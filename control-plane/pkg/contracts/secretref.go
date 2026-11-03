package contracts

import "github.com/agentoven/agentoven/control-plane/internal/secretref"

// KitchenSecretName is the one check on a secret reference that a person in a kitchen typed: it must be
// vault://<name>, the name of a credential in that kitchen's vault, and the result is that name. An
// env:// or k8s:// reference is refused because it would read the installation's own environment or
// cluster secrets. Every edition's code that accepts a reference from a kitchen (a trigger, a pack, a
// channel, a bot, a judge) validates with this, and resolves with the server's Resolver.ResolveFor,
// which applies the same rule, so a call site cannot be written that accepts the wider set by mistake.
func KitchenSecretName(ref string) (string, error) { return secretref.KitchenVaultName(ref) }
