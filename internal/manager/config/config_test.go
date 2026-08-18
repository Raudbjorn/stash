package config

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/stashapp/stash/pkg/scene/metadata/entity"
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

func TestConfigAITaggingAnalyzeModeRequiresTaxonomyOptIn(t *testing.T) {
	i := InitializeEmpty()
	assert.Equal(t, "legacy", i.GetAITaggingAnalyzeMode())

	i.SetString(AITaggingAnalyzeMode, "taxonomy")
	assert.Equal(t, "taxonomy", i.GetAITaggingAnalyzeMode())

	i.SetString(AITaggingAnalyzeMode, "invalid")
	assert.Equal(t, "legacy", i.GetAITaggingAnalyzeMode())
}

func TestConfigAITaggingVoyageDimensionUsesRESTDefault(t *testing.T) {
	i := InitializeEmpty()
	assert.Equal(t, 1024, i.GetAITaggingVLMVoyageDimension())

	// Earlier builds persisted 256 even though the REST endpoint always
	// returned its 1024-dimensional default. Ignore that stale setting.
	i.SetInt(AITaggingVLMVoyageDimension, 256)
	assert.Equal(t, 1024, i.GetAITaggingVLMVoyageDimension())
}

func TestSceneMetadataModelConfiguration(t *testing.T) {
	i := InitializeEmpty()
	assert.Empty(t, i.GetSceneMetadataEntityModel())
	assert.Equal(t, map[entity.Role]string{
		entity.RoleEntityExtraction: "gliner-small-v2.1-int8",
	}, i.GetSceneMetadataEntityModelAssignments())

	i.SetSceneMetadataEntityModel("gliner-medium-v2.1-int8")
	assert.Equal(t, "gliner-medium-v2.1-int8", i.GetSceneMetadataEntityModel())

	err := i.SetSceneMetadataEntityModelAssignments(map[entity.Role]string{
		entity.RoleEntityExtraction: "gliner-medium-v2.1-int8",
		entity.RolePerformerContext: "gliner-large-v2.1-int8",
	})
	assert.NoError(t, err)
	assert.Equal(t, map[entity.Role]string{
		entity.RoleEntityExtraction: "gliner-medium-v2.1-int8",
		entity.RolePerformerContext: "gliner-large-v2.1-int8",
	}, i.GetSceneMetadataEntityModelAssignments())

	assert.NoError(t, i.SetSceneMetadataEntityModelAssignments(map[entity.Role]string{
		entity.RoleEntityExtraction: "",
	}))
	assert.Equal(t, map[entity.Role]string{
		entity.RoleEntityExtraction: "",
	}, i.GetSceneMetadataEntityModelAssignments())
}

func TestSceneMetadataModelConfigurationDropsUnknownKeys(t *testing.T) {
	i := InitializeEmpty()
	i.SetString(SceneMetadataEntityModelAssignment, `{"entity_extraction":"not-in-catalog"}`)
	assert.Equal(t, map[entity.Role]string{
		entity.RoleEntityExtraction: "gliner-small-v2.1-int8",
	}, i.GetSceneMetadataEntityModelAssignments())
}
