package guardrails

// Check-digit and format validators for the detectors in detectors.go. A pattern alone matches
// any string of the right shape, and most 16-digit strings are not card numbers; the validators
// are what keep a redacting rule from mangling order numbers and timestamps. Each one takes the
// text a pattern matched and reports whether it is a real value of its kind. Every algorithm
// here is public (ISO 7064, Luhn, Verhoeff, the national schemes' own published rules).

import (
	"encoding/base64"
	"encoding/json"
	"net/netip"
	"strconv"
	"strings"
)

// digitsOf returns the decimal digits of s as numbers, ignoring every other character.
func digitsOf(s string) []int {
	out := make([]int, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			out = append(out, int(s[i]-'0'))
		}
	}
	return out
}

// compact is a code in capitals without the separators an issuer's grouping may carry (space, hyphen,
// dot, slash, line break), which is what the check digits are computed over.
func compact(s string) string {
	return strings.ToUpper(strings.NewReplacer(" ", "", "-", "", ".", "", "/", "", "\n", "").Replace(s))
}

// luhnValid is the Luhn check over every digit of s.
func luhnValid(digits []int) bool {
	if len(digits) < 2 {
		return false
	}
	sum := 0
	double := false
	for i := len(digits) - 1; i >= 0; i-- {
		d := digits[i]
		if double {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
		double = !double
	}
	return sum%10 == 0
}

// cardValid accepts a payment card number: 13 to 19 digits that pass Luhn and begin with an
// issuer prefix of the right length. The prefix test is what rejects millisecond timestamps
// (13 digits starting with 1, which pass Luhn one time in ten) and other long numbers.
func cardValid(match string) bool {
	d := digitsOf(match)
	n := len(d)
	if n < 13 || n > 19 || !luhnValid(d) {
		return false
	}
	allSame := true
	for _, x := range d {
		if x != d[0] {
			allSame = false
			break
		}
	}
	if allSame {
		return false
	}
	p2 := d[0]*10 + d[1]
	p4 := p2*100 + d[2]*10 + d[3]
	switch {
	case d[0] == 4: // Visa
		return n == 13 || n == 16 || n == 19
	case p2 == 34 || p2 == 37: // American Express
		return n == 15
	case (p2 >= 51 && p2 <= 55) || (p4 >= 2221 && p4 <= 2720): // Mastercard
		return n == 16
	case p2 == 36 || p2 == 38 || p2 == 39 || (p4 >= 3000 && p4 <= 3059): // Diners Club
		return n >= 14 && n <= 19
	case p2 == 35: // JCB
		return n >= 16 && n <= 19
	case p2 == 62 || p2 == 65 || p4 == 6011 || (p4 >= 6440 && p4 <= 6499): // UnionPay, Discover
		return n >= 16 && n <= 19
	case d[0] == 5 || d[0] == 6: // other Maestro and local schemes
		return n >= 13 && n <= 19
	}
	return false
}

// ssnValid applies the Social Security Administration's rules for an issued number: the area is
// not 000, 666 or 900-999, the group is not 00 and the serial is not 0000.
func ssnValid(match string) bool {
	d := digitsOf(match)
	if len(d) != 9 {
		return false
	}
	area := d[0]*100 + d[1]*10 + d[2]
	group := d[3]*10 + d[4]
	serial := d[5]*1000 + d[6]*100 + d[7]*10 + d[8]
	return area != 0 && area != 666 && area < 900 && group != 0 && serial != 0
}

// ibanLengths is the length of an IBAN by country (ISO 13616 registry).
var ibanLengths = map[string]int{
	"AD": 24, "AE": 23, "AL": 28, "AT": 20, "AZ": 28, "BA": 20, "BE": 16, "BG": 22, "BH": 22, "BR": 29,
	"BY": 28, "CH": 21, "CR": 22, "CY": 28, "CZ": 24, "DE": 22, "DK": 18, "DO": 28, "EE": 20, "EG": 29,
	"ES": 24, "FI": 18, "FO": 18, "FR": 27, "GB": 22, "GE": 22, "GI": 23, "GL": 18, "GR": 27, "GT": 28,
	"HR": 21, "HU": 28, "IE": 22, "IL": 23, "IQ": 23, "IS": 26, "IT": 27, "JO": 30, "KW": 30, "KZ": 20,
	"LB": 28, "LC": 32, "LI": 21, "LT": 20, "LU": 20, "LV": 21, "MC": 27, "MD": 24, "ME": 22, "MK": 19,
	"MR": 27, "MT": 31, "MU": 30, "NL": 18, "NO": 15, "PK": 24, "PL": 28, "PS": 29, "PT": 25, "QA": 29,
	"RO": 24, "RS": 22, "SA": 24, "SC": 31, "SE": 24, "SI": 19, "SK": 24, "SM": 27, "ST": 25, "SV": 28,
	"TL": 23, "TN": 24, "TR": 26, "UA": 29, "VA": 22, "VG": 24, "XK": 20,
}

// ibanValid checks the country's length and the ISO 7064 MOD 97-10 check digits.
func ibanValid(match string) bool {
	s := compact(match)
	if len(s) < 15 || len(s) > 34 {
		return false
	}
	if want, ok := ibanLengths[s[:2]]; !ok || want != len(s) {
		return false
	}
	// Move the first four characters to the end and read letters as 10..35. The number is
	// too long for an integer, so the remainder is carried digit by digit.
	rearranged := s[4:] + s[:4]
	rem := 0
	for i := 0; i < len(rearranged); i++ {
		c := rearranged[i]
		switch {
		case c >= '0' && c <= '9':
			rem = (rem*10 + int(c-'0')) % 97
		case c >= 'A' && c <= 'Z':
			rem = (rem*100 + int(c-'A') + 10) % 97
		default:
			return false
		}
	}
	return rem == 1
}

// Verhoeff's tables (the dihedral group D5), the check Aadhaar numbers use.
var (
	verhoeffD = [10][10]int{
		{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}, {1, 2, 3, 4, 0, 6, 7, 8, 9, 5}, {2, 3, 4, 0, 1, 7, 8, 9, 5, 6},
		{3, 4, 0, 1, 2, 8, 9, 5, 6, 7}, {4, 0, 1, 2, 3, 9, 5, 6, 7, 8}, {5, 9, 8, 7, 6, 0, 4, 3, 2, 1},
		{6, 5, 9, 8, 7, 1, 0, 4, 3, 2}, {7, 6, 5, 9, 8, 2, 1, 0, 4, 3}, {8, 7, 6, 5, 9, 3, 2, 1, 0, 4},
		{9, 8, 7, 6, 5, 4, 3, 2, 1, 0},
	}
	verhoeffP = [8][10]int{
		{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}, {1, 5, 7, 6, 2, 8, 3, 0, 9, 4}, {5, 8, 0, 3, 7, 9, 6, 1, 4, 2},
		{8, 9, 1, 6, 0, 4, 3, 5, 2, 7}, {9, 4, 5, 3, 1, 2, 6, 8, 7, 0}, {4, 2, 8, 6, 5, 7, 3, 9, 0, 1},
		{2, 7, 9, 3, 8, 0, 6, 4, 1, 5}, {7, 0, 4, 6, 9, 1, 3, 2, 5, 8},
	}
)

func verhoeffValid(digits []int) bool {
	c := 0
	for i := 0; i < len(digits); i++ {
		c = verhoeffD[c][verhoeffP[i%8][digits[len(digits)-1-i]]]
	}
	return c == 0
}

// aadhaarValid: twelve digits, the first 2 to 9 (UIDAI does not issue 0 or 1), Verhoeff check.
// A card number's first twelve digits would also fit, which is why the longer span wins.
func aadhaarValid(match string) bool {
	d := digitsOf(match)
	return len(d) == 12 && d[0] >= 2 && verhoeffValid(d)
}

// panValid: the fourth character of a PAN is the holder type, one of a fixed set.
func panValid(match string) bool {
	m := compact(match)
	return len(m) == 10 && strings.IndexByte("ABCFGHLJPTK", m[3]) >= 0
}

// ninoValid rejects the prefixes HMRC never issues. The pattern already excludes the letters
// that never occur in a prefix.
func ninoValid(match string) bool {
	s := compact(match)
	if len(s) != 9 {
		return false
	}
	switch s[:2] {
	case "BG", "GB", "NK", "KN", "TN", "NT", "ZZ":
		return false
	}
	return true
}

// nhsValid: ten digits; the tenth is 11 minus the weighted sum (weights 10 down to 2) mod 11,
// where 11 reads as 0 and 10 means the number is not valid.
func nhsValid(match string) bool {
	d := digitsOf(match)
	if len(d) != 10 {
		return false
	}
	sum := 0
	for i := 0; i < 9; i++ {
		sum += d[i] * (10 - i)
	}
	check := 11 - sum%11
	if check == 11 {
		check = 0
	}
	return check != 10 && check == d[9]
}

// vatValid checks the national check digits of a VAT number for the countries whose scheme is
// public (DE, FR, IT, BE, PL). The pattern only produces those prefixes.
func vatValid(match string) bool {
	s := compact(match)
	if len(s) < 4 {
		return false
	}
	d := digitsOf(s[2:])
	switch s[:2] {
	case "DE":
		// ISO 7064 MOD 11,10 over the first eight digits.
		if len(d) != 9 || d[0] == 0 {
			return false
		}
		product := 10
		for i := 0; i < 8; i++ {
			sum := (d[i] + product) % 10
			if sum == 0 {
				sum = 10
			}
			product = (2 * sum) % 11
		}
		check := 11 - product
		if check == 10 {
			check = 0
		}
		return check == d[8]
	case "FR":
		// Two key digits, then the nine-digit SIREN: key = (12 + 3 * (siren mod 97)) mod 97.
		if len(d) != 11 {
			return false
		}
		siren, _ := strconv.Atoi(s[4:])
		key, _ := strconv.Atoi(s[2:4])
		return (12+3*(siren%97))%97 == key
	case "IT":
		return len(d) == 11 && luhnValid(d) && !(d[0] == 0 && d[1] == 0 && d[2] == 0 && d[3] == 0 && d[4] == 0 && d[5] == 0 && d[6] == 0)
	case "BE":
		if len(d) != 10 {
			return false
		}
		n, _ := strconv.Atoi(s[2:10])
		check, _ := strconv.Atoi(s[10:12])
		return 97-n%97 == check
	case "PL":
		if len(d) != 10 {
			return false
		}
		w := [9]int{6, 5, 7, 2, 3, 4, 5, 6, 7}
		sum := 0
		for i := 0; i < 9; i++ {
			sum += d[i] * w[i]
		}
		return sum%11 == d[9] && sum%11 != 10
	}
	return false
}

// dniValid: a Spanish DNI (eight digits) or NIE (X, Y or Z then seven digits) ends in a letter
// chosen by the number mod 23.
func dniValid(match string) bool {
	s := strings.ToUpper(match)
	if len(s) != 9 {
		return false
	}
	num := s[:8]
	switch s[0] {
	case 'X':
		num = "0" + s[1:8]
	case 'Y':
		num = "1" + s[1:8]
	case 'Z':
		num = "2" + s[1:8]
	}
	n, err := strconv.Atoi(num)
	if err != nil {
		return false
	}
	return "TRWAGMYFPDXBNJZSQVHLCKE"[n%23] == s[8]
}

// bsnValid: the Dutch citizen service number passes the "11-proof".
func bsnValid(match string) bool {
	d := digitsOf(match)
	if len(d) != 9 {
		return false
	}
	sum := 0
	for i := 0; i < 8; i++ {
		sum += d[i] * (9 - i)
	}
	sum -= d[8]
	return sum%11 == 0 && sum != 0
}

// phoneIntlValid: an international number has 8 to 15 digits (E.164 allows 15), and is not one
// digit repeated.
func phoneIntlValid(match string) bool {
	d := digitsOf(match)
	if len(d) < 8 || len(d) > 15 {
		return false
	}
	for _, x := range d {
		if x != d[0] {
			return true
		}
	}
	return false
}

// ipValid accepts a dotted-quad or an IPv6 address the standard library parses. An IPv6
// candidate needs a hex digit so that "::" in code is not an address.
func ipValid(match string) bool {
	a, err := netip.ParseAddr(match)
	if err != nil {
		return false
	}
	if a.Is4() {
		return true
	}
	return strings.ContainsAny(match, "0123456789abcdefABCDEF")
}

// jwtValid: the header of a JSON Web Token is base64url JSON naming an algorithm.
func jwtValid(match string) bool {
	head, _, ok := strings.Cut(match, ".")
	if !ok {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(head, "="))
	if err != nil {
		return false
	}
	var h map[string]interface{}
	if json.Unmarshal(raw, &h) != nil {
		return false
	}
	_, hasAlg := h["alg"]
	return hasAlg
}
