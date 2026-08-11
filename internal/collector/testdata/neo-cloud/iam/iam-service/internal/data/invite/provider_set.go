package invite

import "github.com/google/wire"

// ProviderSet wires the invite GORM repositories.
var ProviderSet = wire.NewSet(
	NewInviteCodeRepo,
	NewPersonalRelationRepo,
	NewOrgInviteLinkRepo,
	NewOrgJoinApplicationRepo,
	NewInviteConfigRepo,
)
