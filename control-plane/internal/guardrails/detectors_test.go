package guardrails

import (
	"strings"
	"testing"
)

// Each case is a value of its kind that must be found, or a near miss that must not.
func TestDetectorsAcceptRealValuesAndRejectLookalikes(t *testing.T) {
	cases := []struct {
		detector string
		valid    []string
		invalid  []string
	}{
		{"credit_card",
			[]string{"4111111111111111", "4111 1111 1111 1111", "5555-5555-5555-4444", "378282246310005", "3782 822463 10005", "6011111111111117", "4222222222222"},
			[]string{"4111111111111112", "1700000000000", "0000000000000000", "1234-5678-9012-3456"}},
		{"iban",
			[]string{"DE89370400440532013000", "DE89 3704 0044 0532 0130 00", "GB82WEST12345698765432", "FR1420041010050500013M02606"},
			[]string{"DE89370400440532013001", "DE8937040044053201300", "XX89370400440532013000"}},
		{"aadhaar",
			[]string{"234567890124", "2345 6789 0124"},
			[]string{"234567890125", "123456789012" /* leading 1 is never issued */}},
		{"pan_in", []string{"ABCPE1234F", "abcpe1234f"}, []string{"ABCXE1234F"}},
		{"uk_nino", []string{"AB123456C", "AB 12 34 56 C", "JG103759A"}, []string{"BG123456A", "DA123456A", "AB123456E"}},
		{"nhs_number", []string{"9434765919", "943 476 5919"}, []string{"9434765918", "9434765910"}},
		{"eu_vat",
			[]string{"DE136695976", "FR83404833048", "IT00743110157", "BE0123456749", "PL5260250995"},
			[]string{"DE136695977", "FR00404833048", "IT00743110158", "BE0123456748", "PL5260250996"}},
		{"es_dni", []string{"12345678Z", "X1234567L"}, []string{"12345678A", "X1234567A"}},
		{"ssn", []string{"123-45-6789"}, []string{"000-12-3456", "666-12-3456", "900-12-3456", "123-00-6789", "123-45-0000"}},
		{"ip_address", []string{"192.168.1.10", "8.8.8.8"}, []string{"256.1.1.1", "1.2.3"}},
		{"ipv6_address", []string{"see 2001:db8::1 now", "::1", "fe80::1ff:fe23:4567:890a"}, []string{"std::vector", "12:30:45", "aa:bb:cc:dd:ee:ff"}},
		{"phone_intl", []string{"+44 20 7946 0958", "+1 (415) 555-2671", "+919876543210"}, []string{"+1234", "+11111111"}},
		{"email", []string{"ann@example.com"}, []string{"ann@localhost", "not an email"}},
		{"aws_access_key", []string{"AKIAIOSFODNN7EXAMPLE"}, []string{"AKIAIOSFODNN7EXAMPL", "AKIAiosfodnn7example"}},
		{"github_token", []string{"ghp_" + strings.Repeat("a1B2", 9), "github_pat_" + strings.Repeat("x", 60)}, []string{"ghp_short"}},
		{"slack_token", []string{"xoxb-1234567890-abcdefghij"}, []string{"xoxb-1"}},
		{"jwt", []string{"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U"},
			[]string{"eyJhbGFhbGFhbGE.eyJhbGFhbGFhbGE.abcdefghijk" /* header is not JSON */}},
		{"private_key", []string{"-----BEGIN RSA PRIVATE KEY-----\nMIIBOgIBAAJBAK\n-----END RSA PRIVATE KEY-----", "-----BEGIN PRIVATE KEY-----\ncut off"}, []string{"-----BEGIN PUBLIC KEY-----\nabc\n-----END PUBLIC KEY-----"}},
		{"api_key", []string{"sk-ant-api03-abcdefghijklmnopqrstuvwx", "AIza" + strings.Repeat("a", 35), "sk_live_" + strings.Repeat("a", 24)}, []string{"sk-short", "task-management-system-overview"}},
	}
	for _, c := range cases {
		det := detectors[c.detector]
		if det == nil {
			t.Fatalf("no detector %q", c.detector)
		}
		for _, v := range c.valid {
			if got, _ := findSpans([]*detector{det}, v, nil, nil); len(got) == 0 {
				t.Errorf("%s: should find %q", c.detector, v)
			}
		}
		for _, v := range c.invalid {
			if got, _ := findSpans([]*detector{det}, v, nil, nil); len(got) != 0 {
				t.Errorf("%s: should not find %q (found %q)", c.detector, v, v[got[0].start:got[0].end])
			}
		}
	}
}

func TestOverlappingFindingsKeepTheLongerAndMoreSpecific(t *testing.T) {
	dets, _ := resolveDetectors([]string{"phone", "credit_card", "nhs_number", "aadhaar"})
	text := "card 4111 1111 1111 1111 end"
	spans, _ := findSpans(dets, text, nil, nil)
	if len(spans) != 1 || spans[0].det.name != "credit_card" || text[spans[0].start:spans[0].end] != "4111 1111 1111 1111" {
		t.Fatalf("a card number must be one finding, not also a phone number inside it: %+v", spans)
	}
}

func TestAllowlistLeavesListedValuesAndDomainsAlone(t *testing.T) {
	dets, _ := resolveDetectors([]string{"email", "credit_card"})
	al := newAllowlist([]string{"4111 1111 1111 1111", "Support@Example.com"}, []string{"@corp.example"})
	text := "mail support@example.com, ann@corp.example, bob@x.corp.example, eve@other.com, card 4111-1111-1111-1111"
	spans, _ := findSpans(dets, text, al, nil)
	if len(spans) != 1 || text[spans[0].start:spans[0].end] != "eve@other.com" {
		t.Fatalf("only the unlisted address should remain: %+v", spans)
	}
}

func TestLegacyDefaultsAreTheFourOriginalDetectors(t *testing.T) {
	dets, err := resolveDetectors(nil)
	if err != nil || len(dets) != 4 {
		t.Fatalf("an empty list means email, phone, ssn, credit_card: %v %v", dets, err)
	}
	if _, err := resolveDetectors([]string{"iban", "nope"}); err == nil {
		t.Fatal("an unknown name must be an error")
	}
	all, _ := resolveDetectors([]string{"all"})
	if len(all) < 15 {
		t.Fatalf("the all group should hold every pii and secret detector, got %d", len(all))
	}
}
