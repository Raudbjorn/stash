package entity

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCatalogPinsAllChecksums(t *testing.T) {
	require.Len(t, Catalog, 4)
	for _, spec := range Catalog {
		t.Run(spec.Key, func(t *testing.T) {
			require.Len(t, spec.Artifacts, 8)
			for _, artifact := range spec.Artifacts {
				assert.NotEmpty(t, artifact.RemotePath)
				assert.NotEmpty(t, artifact.LocalPath)
				assert.Positive(t, artifact.Size)
				assert.Len(t, artifact.SHA256, 64)
			}
		})
	}
}
func TestCatalogPinsRepositorySpecificSidecars(t *testing.T) {
	expected := map[string]map[string]Artifact{
		"gliner-medium-v2.1-int8": {
			"gliner_config.json": {Size: 730, SHA256: "7a45664d36377a1cd8893424dedb61b466a1541213c9d47324ed5bb32a5c6aeb"},
		},
		"gliner-large-v2.1-int8": {
			"gliner_config.json": {Size: 731, SHA256: "ed6a6099d4f85a104214b83b91d48246d2c18b1b3f0288880ffe01a321d31f9c"},
		},
		"gliner-multi-v2.1-int8": {
			"tokenizer.json":        {Size: 16331948, SHA256: "914bd3c8fb7b525af9e23b60d0ec7b1248ddb2b99014efd9c02ebeb022f8cab7"},
			"spm.model":             {Size: 4305025, SHA256: "13c8d666d62a7bc4ac8f040aab68e942c861f93303156cc28f5c7e885d86d6e3"},
			"gliner_config.json":    {Size: 731, SHA256: "1ef59c57fe6816a155697a5670ca8d0faf0babe13f199a2c2b5b65113399ed72"},
			"added_tokens.json":     {Size: 86, SHA256: "030e747c4ca7992a3ac794c6fda9919352c88ae722e85178217cd083b450078d"},
			"tokenizer_config.json": {Size: 1806, SHA256: "78f866883daf7ee2bc400200a155cdbe9116ed0a6ed597ff573ea2c9862a89a6"},
		},
	}

	for modelKey, expectedArtifacts := range expected {
		spec, ok := FindModel(modelKey)
		require.True(t, ok)
		actual := make(map[string]Artifact, len(spec.Artifacts))
		for _, artifact := range spec.Artifacts {
			actual[artifact.LocalPath] = artifact
		}
		for localPath, expectedArtifact := range expectedArtifacts {
			assert.Equal(t, expectedArtifact.Size, actual[localPath].Size, "%s %s size", modelKey, localPath)
			assert.Equal(t, expectedArtifact.SHA256, actual[localPath].SHA256, "%s %s checksum", modelKey, localPath)
		}
	}
}

func TestCatalogKeyUnique(t *testing.T) {
	seen := make(map[string]struct{}, len(Catalog))
	for _, spec := range Catalog {
		_, duplicate := seen[spec.Key]
		assert.False(t, duplicate, "duplicate catalog key %q", spec.Key)
		seen[spec.Key] = struct{}{}
	}
}

func TestFindModelReturnsIndependentSlices(t *testing.T) {
	spec, ok := FindModel("gliner-small-v2.1-int8")
	require.True(t, ok)
	spec.Artifacts[0].SHA256 = "changed"
	spec.Labels[0] = "changed"

	fresh, ok := FindModel("gliner-small-v2.1-int8")
	require.True(t, ok)
	assert.NotEqual(t, "changed", fresh.Artifacts[0].SHA256)
	assert.NotEqual(t, "changed", fresh.Labels[0])
}
