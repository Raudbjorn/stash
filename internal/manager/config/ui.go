package config

type ConfigDisableDropdownCreate struct {
	Performer bool `json:"performer"`
	Tag       bool `json:"tag"`
	Studio    bool `json:"studio"`
	Movie     bool `json:"movie"`
	Gallery   bool `json:"gallery"`
}
