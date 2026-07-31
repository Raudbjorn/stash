package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestConfig_GetAllPluginConfiguration(t *testing.T) {
	i := InitializeEmpty()

	assert.Equal(t, i.GetAllPluginConfiguration(), map[string]map[string]interface{}{})

	i.SetPluginConfiguration("plugin1", map[string]interface{}{"key1": "value1"})

	assert.Equal(t, map[string]map[string]interface{}{
		"plugin1": {"key1": "value1"},
	}, i.GetAllPluginConfiguration())

	i.SetPluginConfiguration("plugin2", map[string]interface{}{"key2": "value2"})

	assert.Equal(t, map[string]map[string]interface{}{
		"plugin1": {"key1": "value1"},
		"plugin2": {"key2": "value2"},
	}, i.GetAllPluginConfiguration())

	// ensure SetPluginConfiguration overwrites existing configuration
	i.SetPluginConfiguration("plugin2", map[string]interface{}{"key3": "value3"})

	assert.Equal(t, map[string]map[string]interface{}{
		"plugin1": {"key1": "value1"},
		"plugin2": {"key3": "value3"},
	}, i.GetAllPluginConfiguration())
}

func TestConfigAITaggingVLMDefaultsAndOverrides(t *testing.T) {
	i := InitializeEmpty()
	assert.Equal(t, "internvl3-2b", i.GetAITaggingVLMModel())
	assert.Empty(t, i.GetAITaggingVLMLabels())
	assert.Zero(t, i.GetAITaggingVLMGPULayers())
	assert.Zero(t, i.GetAITaggingVLMContext())

	i.SetString(AITaggingVLMModel, "qwen25-vl-7b")
	i.SetInterface(AITaggingVLMLabels, []string{"tag one", "tag two"})
	i.SetInt(AITaggingVLMGPULayers, 17)
	i.SetInt(AITaggingVLMContext, 4096)

	assert.Equal(t, "qwen25-vl-7b", i.GetAITaggingVLMModel())
	assert.Equal(t, []string{"tag one", "tag two"}, i.GetAITaggingVLMLabels())
	assert.Equal(t, 17, i.GetAITaggingVLMGPULayers())
	assert.Equal(t, 4096, i.GetAITaggingVLMContext())
}
