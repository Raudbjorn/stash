package entity

// Role identifies a scene-metadata capability that may be assigned a model.
type Role string

const (
	RoleEntityExtraction        Role = "entity_extraction"
	RolePerformerContext        Role = "performer_context"
	RoleStudioProviderSelection Role = "studio_provider_selection"
)

func ValidRole(role Role) bool {
	switch role {
	case RoleEntityExtraction, RolePerformerContext, RoleStudioProviderSelection:
		return true
	default:
		return false
	}
}
