package api_test

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/agentoven/agentoven/control-plane/pkg/kitchentest"
	"github.com/agentoven/agentoven/control-plane/pkg/server"
	"github.com/go-chi/chi/v5"
	"net/http"
	"net/http/httptest"

	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/pkg/contracts"
)

// newKitchenTestServer is the whole community server over its in-memory store, with an API key for auth.
func newKitchenTestServer(t *testing.T) *server.Server {
	t.Helper()
	t.Setenv("AGENTOVEN_API_KEYS", kitchentest.APIKey)
	t.Setenv("AGENTOVEN_REQUIRE_AUTH", "true")
	t.Setenv("AGENTOVEN_DATA_DIR", t.TempDir())
	srv, err := server.New(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

// sessStore, newByIDRouter and call are the helpers content_routes_test.go builds its server and requests with.
var sessStore contracts.SessionStore

func newByIDRouter(t *testing.T) (http.Handler, store.Store, *chi.Mux) {
	t.Helper()
	srv := newKitchenTestServer(t)
	sessStore = srv.SessionStore
	return srv.Handler, srv.Store, srv.Mux
}

func call(h http.Handler, kitchen, method, path, body string) *httptest.ResponseRecorder {
	return kitchentest.Do(h, kitchen, method, path, body)
}

// TestByIDRoutesAreKitchenScoped is the generic cross-kitchen test (the same walk Pro runs against PostgreSQL):
// a caller in another kitchen names the id of a record on every by-id route and is refused as if it did not exist.
func TestByIDRoutesAreKitchenScoped(t *testing.T) { kitchentest.RunByID(t, newKitchenTestServer(t)) }

// TestRecipeRunFamilyWorksForItsKitchenAndNotForAnother runs a recipe to a human gate with the real engine.
func TestRecipeRunFamilyWorksForItsKitchenAndNotForAnother(t *testing.T) {
	kitchentest.RunRecipeFlow(t, newKitchenTestServer(t))
}

// TestEveryRouteWithAParameterIsClassified fails for a route that takes a path parameter and is neither a
// by-id route covered by kitchentest.ByIDRoutes nor a by-name route: the person adding it has to say which it
// is, and if it is by id, add it there and see it refused across kitchens.
func TestEveryRouteWithAParameterIsClassified(t *testing.T) {
	srv := newKitchenTestServer(t)
	var unclassified []string
	_ = chi.Walk(srv.Mux, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		route = strings.TrimSuffix(strings.ReplaceAll(route, "/*/", "/"), "/")
		if strings.Contains(route, "{") && strings.HasPrefix(route, "/api/v1/") && !kitchentest.Classified(method, route) {
			unclassified = append(unclassified, method+" "+route)
		}
		return nil
	})
	sort.Strings(unclassified)
	if len(unclassified) > 0 {
		t.Fatalf("routes with a path parameter that are neither by-id (kitchentest.ByIDRoutes) nor by-name (ByNamePrefixes, OtherParamRoutes):\n  %s", strings.Join(unclassified, "\n  "))
	}
}
