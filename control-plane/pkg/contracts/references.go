package contracts

import "context"

// KitchenReferences lists what only an edition can say a kitchen has, for the checks that refuse an agent
// or a recipe that names something absent (pkg/refcheck). The community edition installs none: it has no
// scenarios and no roles, so a reference to one is not checked there. Pro installs one that reads its
// scenario repository and its role table.
//
// Both lists are for the kitchen named: another kitchen's scenario does not exist here.
type KitchenReferences interface {
	// Scenarios lists the ids of the kitchen's evaluation scenarios.
	Scenarios(ctx context.Context, kitchen string) ([]string, error)
	// Roles lists the role names that may be named as an approver (a recipe gate's approver_roles).
	Roles(ctx context.Context, kitchen string) ([]string, error)
}
