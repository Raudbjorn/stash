package models

// ClipCreateInput is the input for creating a clip.
type ClipCreateInput struct {
	Title      *string  `json:"title"`
	SceneID    string   `json:"scene_id"`
	Seconds    float64  `json:"seconds"`
	EndSeconds *float64 `json:"end_seconds"`
	// rating expressed as 1-100
	Rating100 *int     `json:"rating100"`
	TagIds    []string `json:"tag_ids"`
}

// ClipUpdateInput is the input for updating a clip.
type ClipUpdateInput struct {
	ID         string   `json:"id"`
	Title      *string  `json:"title"`
	SceneID    *string  `json:"scene_id"`
	Seconds    *float64 `json:"seconds"`
	EndSeconds *float64 `json:"end_seconds"`
	// rating expressed as 1-100
	Rating100 *int     `json:"rating100"`
	TagIds    []string `json:"tag_ids"`
}

// ClipFilterType is the filter for querying clips.
type ClipFilterType struct {
	OperatorFilter[ClipFilterType]
	Title *StringCriterionInput `json:"title"`
	// Filter to only include clips with these scenes
	Scenes *MultiCriterionInput `json:"scenes"`
	// Filter to only include clips with these tags
	Tags *HierarchicalMultiCriterionInput `json:"tags"`
	// Filter by rating expressed as 1-100
	Rating100 *IntCriterionInput `json:"rating100"`
	// Filter by created at
	CreatedAt *TimestampCriterionInput `json:"created_at"`
	// Filter by updated at
	UpdatedAt *TimestampCriterionInput `json:"updated_at"`
}
