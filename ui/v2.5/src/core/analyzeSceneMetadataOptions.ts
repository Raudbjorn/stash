import * as GQL from "src/core/generated-graphql";

export const ANALYZE_SCENE_METADATA_UI_KEY = "taskDefaults.analyzeSceneMetadata";

export interface IAnalyzeSceneMetadataTaskDefaults {
  dryRun: boolean;
  performerVerifierScraperIDs: string[];
  performerVerifierStashBoxEndpoints: string[];
  performerConfidenceThreshold: number;
  dateConfidenceThreshold: number;
  overwriteExistingDate: boolean;
  overwriteExistingTitle: boolean;
  useDetails: boolean;
  useLocalAIContext: boolean;
  studioVerifierScraperIDs: string[];
  studioVerifierStashBoxEndpoints: string[];
  useLocalAIStudioProviderSelection: boolean;
  providerPolicies: GQL.SceneMetadataProviderPolicyInput[];
  replaceLocalPerformersFromRemote: boolean;
  ignoreMalePerformers: boolean;
}

export function defaultAnalyzeSceneMetadataOptions(): IAnalyzeSceneMetadataTaskDefaults {
  return {
    dryRun: true,
    performerVerifierScraperIDs: [],
    performerVerifierStashBoxEndpoints: [],
    performerConfidenceThreshold: 0.6,
    dateConfidenceThreshold: 0.6,
    overwriteExistingDate: false,
    overwriteExistingTitle: false,
    useDetails: false,
    useLocalAIContext: false,
    studioVerifierScraperIDs: [],
    studioVerifierStashBoxEndpoints: [],
    useLocalAIStudioProviderSelection: false,
    providerPolicies: [],
    replaceLocalPerformersFromRemote: false,
    ignoreMalePerformers: false,
  };
}

export function toPersistedAnalyzeSceneMetadataOptions(
  options: GQL.AnalyzeSceneMetadataInput | IAnalyzeSceneMetadataTaskDefaults
): IAnalyzeSceneMetadataTaskDefaults {
  const defaults = defaultAnalyzeSceneMetadataOptions();
  return {
    dryRun: options.dryRun ?? defaults.dryRun,
    performerVerifierScraperIDs:
      options.performerVerifierScraperIDs ?? defaults.performerVerifierScraperIDs,
    performerVerifierStashBoxEndpoints:
      options.performerVerifierStashBoxEndpoints ??
      defaults.performerVerifierStashBoxEndpoints,
    performerConfidenceThreshold:
      options.performerConfidenceThreshold ??
      defaults.performerConfidenceThreshold,
    dateConfidenceThreshold:
      options.dateConfidenceThreshold ?? defaults.dateConfidenceThreshold,
    overwriteExistingDate:
      options.overwriteExistingDate ?? defaults.overwriteExistingDate,
    overwriteExistingTitle:
      options.overwriteExistingTitle ?? defaults.overwriteExistingTitle,
    useDetails: options.useDetails ?? defaults.useDetails,
    useLocalAIContext: options.useLocalAIContext ?? defaults.useLocalAIContext,
    studioVerifierScraperIDs:
      options.studioVerifierScraperIDs ?? defaults.studioVerifierScraperIDs,
    studioVerifierStashBoxEndpoints:
      options.studioVerifierStashBoxEndpoints ??
      defaults.studioVerifierStashBoxEndpoints,
    useLocalAIStudioProviderSelection:
      options.useLocalAIStudioProviderSelection ??
      defaults.useLocalAIStudioProviderSelection,
    providerPolicies: options.providerPolicies ?? defaults.providerPolicies,
    replaceLocalPerformersFromRemote:
      options.replaceLocalPerformersFromRemote ??
      defaults.replaceLocalPerformersFromRemote,
    ignoreMalePerformers:
      options.ignoreMalePerformers ?? defaults.ignoreMalePerformers,
  };
}
