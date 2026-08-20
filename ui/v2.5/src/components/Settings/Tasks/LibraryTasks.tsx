import React, { useEffect, useMemo, useState } from "react";
import { FormattedMessage, useIntl } from "react-intl";
import { Badge, Button, Form } from "react-bootstrap";
import {
  mutateMetadataScan,
  mutateMetadataAutoTag,
  mutateMetadataGenerate,
  mutateMetadataDetectSceneCuts,
  mutateMetadataAnalyzeScenes,
  useListPerformerScrapers,
  useListStudioScrapers,
  mutateSceneMetadataModelAssign,
  mutateSceneMetadataModelInstall,
  mutateSceneMetadataModelReload,
  mutateSceneMetadataModelUninstall,
  mutateSceneMetadataModelRepair,
  useSceneMetadataModelAssignments,
  useSceneMetadataModels,
  useSceneMetadataModelStatus,
  mutatePurgeSceneMetadataPlans,
  useAIServerAvailability,
} from "src/core/StashService";
import { withoutTypename } from "src/utils/data";
import { useConfigurationContext } from "src/hooks/Config";
import { useAutoTagTrigger } from "src/hooks/useAutoTagTrigger";
import { IdentifyDialog } from "../../Dialogs/IdentifyDialog/IdentifyDialog";
import * as GQL from "src/core/generated-graphql";
import { DirectorySelectionDialog } from "./DirectorySelectionDialog";
import { ScanOptions } from "./ScanOptions";
import { SceneMetadataPlanReviewModal } from "../../Scenes/SceneMetadataPlanReviewModal";
import { useToast } from "src/hooks/Toast";
import { GenerateOptions } from "./GenerateOptions";
import { SettingSection } from "../SettingSection";
import { BooleanSetting, Setting, SettingGroup } from "../Inputs";
import { ManualLink } from "src/components/Help/context";
import { Icon } from "src/components/Shared/Icon";
import { faQuestionCircle } from "@fortawesome/free-solid-svg-icons";
import {
  AutoTagConfirmDialog,
  AutoTagWarning,
} from "src/components/Shared/AutoTagConfirmDialog";
import { useSettings } from "../context";
import { SelectComponent } from "src/components/Shared/Select";
import { FileSize } from "src/components/Shared/FileSize";

const builtinStudioURLScraperID = "builtin-studio-url-map";

interface IAnalyzeSceneMetadataTaskDefaults {
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
}

interface IAutoTagOptions {
  options: GQL.AutoTagMetadataInput;
  setOptions: (s: GQL.AutoTagMetadataInput) => void;
}

const AutoTagOptions: React.FC<IAutoTagOptions> = ({
  options,
  setOptions: setOptionsState,
}) => {
  const { performers, studios, tags } = options;
  const wildcard = ["*"];

  function set(v?: boolean) {
    if (v) {
      return wildcard;
    }
    return [];
  }

  function setOptions(input: Partial<GQL.AutoTagMetadataInput>) {
    setOptionsState({ ...options, ...input });
  }

  return (
    <>
      <BooleanSetting
        id="autotag-performers"
        checked={!!performers?.length}
        headingID="performers"
        onChange={(v) => setOptions({ performers: set(v) })}
      />
      <BooleanSetting
        id="autotag-studios"
        checked={!!studios?.length}
        headingID="studios"
        onChange={(v) => setOptions({ studios: set(v) })}
      />
      <BooleanSetting
        id="autotag-tags"
        checked={!!tags?.length}
        headingID="tags"
        onChange={(v) => setOptions({ tags: set(v) })}
      />
    </>
  );
};

const SceneMetadataModelsPanel: React.FC = () => {
  const intl = useIntl();
  const Toast = useToast();
  const [busyKey, setBusyKey] = useState<string>();
  const {
    data: modelsData,
    loading: modelsLoading,
    refetch: refetchModels,
  } = useSceneMetadataModels();
  const {
    data: assignmentsData,
    loading: assignmentsLoading,
    refetch: refetchAssignments,
  } = useSceneMetadataModelAssignments();
  const {
    data: statusData,
    loading: statusLoading,
    refetch: refetchStatus,
  } = useSceneMetadataModelStatus();

  const models = modelsData?.sceneMetadataModels ?? [];
  const assignments = assignmentsData?.sceneMetadataModelAssignments ?? [];
  const status = statusData?.sceneMetadataModelStatus;
  const assignedKeys = new Set(
    assignments.flatMap((assignment) =>
      assignment.modelKey ? [assignment.modelKey] : []
    )
  );
  const roleRows: Array<{
    role: GQL.SceneMetadataModelRole;
    label: string;
  }> = [
    {
      role: GQL.SceneMetadataModelRole.EntityExtraction,
      label:
        "config.tasks.analyze_scene_metadata.model.assign_entity_extraction",
    },
    {
      role: GQL.SceneMetadataModelRole.PerformerContext,
      label:
        "config.tasks.analyze_scene_metadata.model.assign_performer_context",
    },
    {
      role: GQL.SceneMetadataModelRole.StudioProviderSelection,
      label:
        "config.tasks.analyze_scene_metadata.model.assign_studio_provider_selection",
    },
  ];

  async function install(modelKey: string) {
    setBusyKey(modelKey);
    try {
      await mutateSceneMetadataModelInstall(modelKey);
      Toast.success(
        intl.formatMessage(
          { id: "config.tasks.added_job_to_queue" },
          {
            operation_name: intl.formatMessage(
              {
                id: "config.tasks.analyze_scene_metadata.model.download",
              },
              { model: modelKey }
            ),
          }
        )
      );
      await Promise.all([refetchModels(), refetchStatus()]);
    } catch (error) {
      Toast.error(error);
    } finally {
      setBusyKey(undefined);
    }
  }

  async function uninstall(modelKey: string) {
    setBusyKey(modelKey);
    try {
      await mutateSceneMetadataModelUninstall(modelKey);
      await Promise.all([refetchModels(), refetchStatus()]);
    } catch (error) {
      Toast.error(error);
    } finally {
      setBusyKey(undefined);
    }
  }

  async function repair(modelKey: string) {
    setBusyKey(modelKey);
    try {
      await mutateSceneMetadataModelRepair(modelKey);
      await Promise.all([refetchModels(), refetchStatus()]);
    } catch (error) {
      Toast.error(error);
    } finally {
      setBusyKey(undefined);
    }
  }

  async function assign(role: GQL.SceneMetadataModelRole, modelKey: string) {
    setBusyKey(role);
    try {
      await mutateSceneMetadataModelAssign(role, modelKey || null);
      await Promise.all([
        refetchAssignments(),
        refetchModels(),
        refetchStatus(),
      ]);
    } catch (error) {
      Toast.error(error);
    } finally {
      setBusyKey(undefined);
    }
  }

  async function reload() {
    setBusyKey("reload");
    try {
      await mutateSceneMetadataModelReload();
      await Promise.all([refetchModels(), refetchStatus()]);
      Toast.success(
        intl.formatMessage({
          id: "config.tasks.analyze_scene_metadata.model.reloaded",
        })
      );
    } catch (error) {
      Toast.error(error);
    } finally {
      setBusyKey(undefined);
    }
  }

  const loading = modelsLoading || assignmentsLoading || statusLoading;
  const runtimeLabel = status?.runtimeAvailable
    ? "config.tasks.analyze_scene_metadata.model.runtime_available"
    : "config.tasks.analyze_scene_metadata.model.runtime_missing";

  return (
    <div className="border rounded p-3 mb-3" aria-live="polite">
      <div className="d-flex flex-wrap align-items-start justify-content-between mb-3">
        <div className="mr-3">
          <Form.Label className="mb-1 font-weight-bold">
            <FormattedMessage id="config.tasks.analyze_scene_metadata.model.catalog_heading" />
          </Form.Label>
          <div className="text-muted small text-break">
            {loading ? (
              <FormattedMessage id="config.tasks.analyze_scene_metadata.model.checking" />
            ) : (
              <FormattedMessage
                id="config.tasks.analyze_scene_metadata.model.status"
                values={{
                  state: status?.state ?? "unknown",
                  cachePath: status?.cachePath ?? "unknown",
                  runtime: intl.formatMessage({ id: runtimeLabel }),
                }}
              />
            )}
          </div>
          {status?.lastError ? (
            <div className="text-danger small mt-1">{status.lastError}</div>
          ) : null}
        </div>
        <Button
          variant="secondary"
          size="sm"
          type="button"
          onClick={reload}
          disabled={
            loading ||
            busyKey !== undefined ||
            !status?.runtimeAvailable ||
            status?.state !== "ready"
          }
        >
          <FormattedMessage id="config.tasks.analyze_scene_metadata.model.reload" />
        </Button>
      </div>

      <div className="mb-4">
        {models.map((model) => {
          const active = model.active;
          const assigned = assignedKeys.has(model.key);
          return (
            <div
              className="d-flex flex-wrap align-items-center justify-content-between border-top py-2"
              key={model.key}
            >
              <div className="mr-3 mb-1">
                <div className="font-weight-bold">{model.displayName}</div>
                <div className="text-muted small">
                  <span className="mr-2">{model.key}</span>
                  <span className="mr-2">{model.family}</span>
                  <span className="mr-2">{model.precision}</span>
                  <span className="mr-2">
                    <FileSize size={model.size} />
                  </span>
                  <a
                    href={model.licenseURL}
                    target="_blank"
                    rel="noopener noreferrer"
                  >
                    {model.license}
                  </a>
                </div>
                {model.lastError ? (
                  <div className="text-danger small">{model.lastError}</div>
                ) : null}
              </div>
              <div className="d-flex align-items-center mb-1">
                <Badge
                  variant={
                    model.installed
                      ? "success"
                      : model.present && !model.validated
                        ? "warning"
                        : "secondary"
                  }
                  className="mr-2"
                >
                  <FormattedMessage
                    id={
                      model.installed
                        ? "config.tasks.analyze_scene_metadata.model.installed"
                        : model.present && !model.validated
                          ? "config.tasks.analyze_scene_metadata.model.present_not_validated"
                          : "config.tasks.analyze_scene_metadata.model.not_installed"
                    }
                  />
                </Badge>
                {active ? (
                  <Badge variant="info" className="mr-2">
                    <FormattedMessage id="config.tasks.analyze_scene_metadata.model.active" />
                  </Badge>
                ) : null}
                {model.present && !model.validated ? (
                  <Button
                    variant="warning"
                    size="sm"
                    type="button"
                    disabled={busyKey !== undefined}
                    onClick={() => repair(model.key)}
                  >
                    <FormattedMessage id="config.tasks.analyze_scene_metadata.model.repair" />
                  </Button>
                ) : !model.present ? (
                  <Button
                    variant="secondary"
                    size="sm"
                    type="button"
                    disabled={
                      busyKey !== undefined || !status?.runtimeAvailable
                    }
                    onClick={() => install(model.key)}
                  >
                    <FormattedMessage
                      id="config.tasks.analyze_scene_metadata.model.download"
                      values={{ model: model.key }}
                    />
                  </Button>
                ) : model.validated && !assigned ? (
                  <Button
                    variant="danger"
                    size="sm"
                    type="button"
                    disabled={busyKey !== undefined}
                    onClick={() => uninstall(model.key)}
                  >
                    <FormattedMessage id="config.tasks.analyze_scene_metadata.model.remove" />
                  </Button>
                ) : null}
              </div>
            </div>
          );
        })}
      </div>

      <div>
        {roleRows.map(({ role, label }) => {
          const assignment = assignments.find((item) => item.role === role);
          const assignableModels = models.filter(
            (model) => model.validated || model.key === assignment?.modelKey
          );
          return (
            <Form.Group controlId={`scene-metadata-model-${role}`} key={role}>
              <Form.Label>
                <FormattedMessage id={label} />
              </Form.Label>
              <Form.Control
                as="select"
                className="input-control"
                value={assignment?.modelKey ?? ""}
                disabled={loading || busyKey !== undefined}
                onChange={(event) => assign(role, event.currentTarget.value)}
              >
                <option value="">
                  {intl.formatMessage({
                    id: "config.tasks.analyze_scene_metadata.model.unset",
                  })}
                </option>
                {assignableModels.map((model) => (
                  <option
                    value={model.key}
                    key={model.key}
                    disabled={!model.validated}
                  >
                    {model.validated
                      ? model.displayName
                      : intl.formatMessage(
                          {
                            id: "config.tasks.analyze_scene_metadata.model.not_installed_option",
                          },
                          { model: model.displayName }
                        )}
                  </option>
                ))}
              </Form.Control>
            </Form.Group>
          );
        })}
        <div className="text-muted small">
          <FormattedMessage id="config.tasks.analyze_scene_metadata.model.assign_requires_install_hint" />
        </div>
        {roleRows.length > 1 ? (
          <div className="text-muted small">
            <FormattedMessage id="config.tasks.analyze_scene_metadata.model.reserved_role_hint" />
          </div>
        ) : null}
      </div>
    </div>
  );
};

export const LibraryTasks: React.FC = () => {
  const intl = useIntl();
  const Toast = useToast();
  const [showReview, setShowReview] = useState(false);
  const [purgeAgeSeconds, setPurgeAgeSeconds] = useState(86400);
  const [purging, setPurging] = useState(false);
  const { ui, saveUI, loading } = useSettings();

  const { taskDefaults } = ui;

  const { data: aiServerData } = useAIServerAvailability();
  const aiAvailable = !!(
    aiServerData?.aiServerStatus.hasVLMProvider ||
    aiServerData?.aiServerStatus.hasLocalTextProvider
  );

  const [dialogOpen, setDialogOpenState] = useState({
    scan: false,
    autoTag: false,
    autoTagAlert: false,
    identify: false,
    generate: false,
  });

  function getDefaultScanOptions(): GQL.ScanMetadataInput {
    return {
      scanGenerateCovers: true,
      scanGeneratePreviews: false,
      scanGenerateImagePreviews: false,
      scanGenerateSprites: false,
      scanGeneratePhashes: false,
      scanGenerateThumbnails: false,
      scanGenerateClipPreviews: false,
    };
  }

  const [scanOptions, setScanOptions] = useState<GQL.ScanMetadataInput>(
    getDefaultScanOptions()
  );
  const [autoTagOptions, setAutoTagOptions] =
    useState<GQL.AutoTagMetadataInput>({
      performers: ["*"],
      studios: ["*"],
      tags: ["*"],
    });

  function getDefaultGenerateOptions(): GQL.GenerateMetadataInput {
    return {
      covers: true,
      sprites: true,
      phashes: true,
      previews: true,
      markers: true,
      previewOptions: {
        previewSegments: 0,
        previewSegmentDuration: 0,
        previewPreset: GQL.PreviewPreset.Slow,
      },
    };
  }

  const [generateOptions, setGenerateOptions] =
    useState<GQL.GenerateMetadataInput>(getDefaultGenerateOptions());

  const [analyzeSceneMetadataOptions, setAnalyzeSceneMetadataOptions] =
    useState<IAnalyzeSceneMetadataTaskDefaults>({
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
    });
  const [
    analyzeSceneMetadataOptionsInitialized,
    setAnalyzeSceneMetadataOptionsInitialized,
  ] = useState(false);
  const {
    data: performerScrapersData,
    error: performerScrapersError,
    loading: performerScrapersLoading,
  } = useListPerformerScrapers();
  const {
    data: studioScrapersData,
    error: studioScrapersError,
    loading: studioScrapersLoading,
  } = useListStudioScrapers();
  const { configuration } = useConfigurationContext();
  const performerVerifierOptions = useMemo(
    () =>
      (performerScrapersData?.listScrapers ?? [])
        .filter((s) =>
          s.performer?.supported_scrapes.includes(GQL.ScrapeType.Name)
        )
        .map((s) => ({ label: s.name, value: s.id })),
    [performerScrapersData]
  );
  const studioVerifierScraperOptions = useMemo(
    () => [
      {
        label: intl.formatMessage({
          id: "config.tasks.analyze_scene_metadata.studio_url_catalog",
        }),
        value: builtinStudioURLScraperID,
      },
      ...(studioScrapersData?.listScrapers ?? [])
        .filter((s) =>
          s.studio?.supported_scrapes.includes(GQL.ScrapeType.Name)
        )
        .map((s) => ({ label: s.name, value: s.id })),
    ],
    [intl, studioScrapersData]
  );
  const stashBoxVerifierOptions = useMemo(
    () =>
      configuration.general.stashBoxes.map((box) => ({
        label: box.name ? `${box.name} — ${box.endpoint}` : box.endpoint,
        value: box.endpoint,
      })),
    [configuration.general.stashBoxes]
  );

  type DialogOpenState = typeof dialogOpen;

  const [configRead, setConfigRead] = useState(false);

  useEffect(() => {
    if (!configuration?.defaults || loading) {
      return;
    }

    const { scan, autoTag } = configuration.defaults;

    // prefer UI defaults over system defaults
    // other defaults should be deprecated
    if (taskDefaults?.scan) {
      setScanOptions(taskDefaults.scan);
    } else if (scan) {
      setScanOptions(withoutTypename(scan));
    }

    if (taskDefaults?.autoTag) {
      setAutoTagOptions(taskDefaults.autoTag);
    } else if (autoTag) {
      setAutoTagOptions(withoutTypename(autoTag));
    }

    if (taskDefaults?.generate) {
      setGenerateOptions(taskDefaults.generate);
    }

    // combine the defaults with the system preview generation settings
    // only do this once
    // don't do this if UI had a default
    if (!configRead && !taskDefaults?.generate) {
      if (configuration?.defaults.generate) {
        const { generate } = configuration.defaults;
        setGenerateOptions(withoutTypename(generate));
      }

      setConfigRead(true);
    }
  }, [configuration, configRead, taskDefaults, loading]);

  useEffect(() => {
    if (
      loading ||
      performerScrapersLoading ||
      performerScrapersError ||
      studioScrapersLoading ||
      studioScrapersError ||
      analyzeSceneMetadataOptionsInitialized
    ) {
      return;
    }

    const persisted = taskDefaults?.analyzeSceneMetadata as
      | Partial<IAnalyzeSceneMetadataTaskDefaults>
      | undefined;
    const requestedIDs = persisted?.performerVerifierScraperIDs ?? [];
    const requestedStashBoxEndpoints =
      persisted?.performerVerifierStashBoxEndpoints ?? [];
    const availableStashBoxEndpoints = new Set(
      stashBoxVerifierOptions.map((option) => option.value)
    );
    const reconciledStashBoxEndpoints = requestedStashBoxEndpoints.filter(
      (endpoint) => availableStashBoxEndpoints.has(endpoint)
    );
    const availableIDs = new Set(
      performerVerifierOptions.map((option) => option.value)
    );
    const reconciledIDs = requestedIDs.filter((id) => availableIDs.has(id));
    const requestedStudioIDs = persisted?.studioVerifierScraperIDs ?? [];
    const requestedStudioStashBoxEndpoints =
      persisted?.studioVerifierStashBoxEndpoints ?? [];
    const reconciledStudioStashBoxEndpoints =
      requestedStudioStashBoxEndpoints.filter((endpoint) =>
        availableStashBoxEndpoints.has(endpoint)
      );
    const availableStudioIDs = new Set(
      studioVerifierScraperOptions.map((option) => option.value)
    );
    const reconciledStudioIDs = requestedStudioIDs.filter((id) =>
      availableStudioIDs.has(id)
    );
    const requestedProviderPolicies = persisted?.providerPolicies ?? [];
    const requestedPolicyEndpoints = new Set(
      requestedProviderPolicies.map((policy) => policy.endpoint)
    );
    const reconciledProviderPolicies = [
      ...requestedProviderPolicies.filter((policy) =>
        availableStashBoxEndpoints.has(policy.endpoint)
      ),
      ...stashBoxVerifierOptions
        .filter((option) => !requestedPolicyEndpoints.has(option.value))
        .map((option) => ({
          endpoint: option.value,
          priority: 0,
          performerMode: GQL.SceneMetadataProviderFieldMode.Observe,
          studioMode: GQL.SceneMetadataProviderFieldMode.Observe,
          dateMode: GQL.SceneMetadataProviderFieldMode.Observe,
          titleMode: GQL.SceneMetadataProviderFieldMode.Observe,
        })),
    ].map((policy, priority) => ({ ...policy, priority }));
    const nextOptions: IAnalyzeSceneMetadataTaskDefaults = {
      dryRun: persisted?.dryRun ?? true,
      performerVerifierScraperIDs: reconciledIDs,
      performerVerifierStashBoxEndpoints: reconciledStashBoxEndpoints,
      performerConfidenceThreshold:
        persisted?.performerConfidenceThreshold ?? 0.6,
      dateConfidenceThreshold: persisted?.dateConfidenceThreshold ?? 0.6,
      overwriteExistingDate: persisted?.overwriteExistingDate ?? false,
      overwriteExistingTitle: persisted?.overwriteExistingTitle ?? false,
      useDetails: persisted?.useDetails ?? false,
      useLocalAIContext: persisted?.useLocalAIContext ?? false,
      studioVerifierScraperIDs: reconciledStudioIDs,
      studioVerifierStashBoxEndpoints: reconciledStudioStashBoxEndpoints,
      useLocalAIStudioProviderSelection:
        persisted?.useLocalAIStudioProviderSelection ?? false,
      providerPolicies: reconciledProviderPolicies,
      replaceLocalPerformersFromRemote:
        persisted?.replaceLocalPerformersFromRemote ?? false,
    };

    setAnalyzeSceneMetadataOptions(nextOptions);
    setAnalyzeSceneMetadataOptionsInitialized(true);

    if (
      !persisted ||
      Object.keys(nextOptions).some(
        (key) =>
          persisted[key as keyof IAnalyzeSceneMetadataTaskDefaults] ===
          undefined
      ) ||
      reconciledIDs.length !== requestedIDs.length ||
      reconciledStashBoxEndpoints.length !==
        requestedStashBoxEndpoints.length ||
      reconciledStudioIDs.length !== requestedStudioIDs.length ||
      reconciledStudioStashBoxEndpoints.length !==
        requestedStudioStashBoxEndpoints.length ||
      JSON.stringify(reconciledProviderPolicies) !==
        JSON.stringify(requestedProviderPolicies)
    ) {
      saveUI({
        taskDefaults: {
          ...taskDefaults,
          analyzeSceneMetadata: nextOptions,
        },
      });
    }
  }, [
    analyzeSceneMetadataOptionsInitialized,
    loading,
    performerScrapersLoading,
    performerScrapersError,
    studioScrapersLoading,
    studioScrapersError,
    performerVerifierOptions,
    studioVerifierScraperOptions,
    stashBoxVerifierOptions,
    taskDefaults,
    saveUI,
  ]);

  function configureDefaults(partial: Record<string, object>) {
    saveUI({ taskDefaults: { ...taskDefaults, ...partial } });
  }

  function onSetScanOptions(s: GQL.ScanMetadataInput) {
    configureDefaults({ scan: s });
    setScanOptions(s);
  }

  function onSetGenerateOptions(s: GQL.GenerateMetadataInput) {
    configureDefaults({ generate: s });
    setGenerateOptions(s);
  }

  function onSetAutoTagOptions(s: GQL.AutoTagMetadataInput) {
    configureDefaults({ autoTag: s });
    setAutoTagOptions(s);
  }

  function persistAnalyzeSceneMetadataOptions(
    options: IAnalyzeSceneMetadataTaskDefaults
  ) {
    configureDefaults({ analyzeSceneMetadata: options });
  }

  function onSetAnalyzeSceneMetadataOptions(
    partial: Partial<IAnalyzeSceneMetadataTaskDefaults>
  ) {
    const nextOptions = { ...analyzeSceneMetadataOptions, ...partial };
    persistAnalyzeSceneMetadataOptions(nextOptions);
    setAnalyzeSceneMetadataOptions(nextOptions);
  }

  function updateProviderPolicy(
    endpoint: string,
    partial: Partial<GQL.SceneMetadataProviderPolicyInput>
  ) {
    onSetAnalyzeSceneMetadataOptions({
      providerPolicies: analyzeSceneMetadataOptions.providerPolicies.map(
        (policy) =>
          policy.endpoint === endpoint ? { ...policy, ...partial } : policy
      ),
    });
  }

  function moveProviderPolicy(sourceEndpoint: string, targetEndpoint: string) {
    if (!sourceEndpoint || sourceEndpoint === targetEndpoint) {
      return;
    }
    const policies = [...analyzeSceneMetadataOptions.providerPolicies];
    const sourceIndex = policies.findIndex(
      (policy) => policy.endpoint === sourceEndpoint
    );
    const targetIndex = policies.findIndex(
      (policy) => policy.endpoint === targetEndpoint
    );
    if (sourceIndex < 0 || targetIndex < 0) {
      return;
    }
    const [source] = policies.splice(sourceIndex, 1);
    policies.splice(targetIndex, 0, source);
    onSetAnalyzeSceneMetadataOptions({
      providerPolicies: policies.map((policy, priority) => ({
        ...policy,
        priority,
      })),
    });
  }

  function setDialogOpen(s: Partial<DialogOpenState>) {
    setDialogOpenState((v) => {
      return { ...v, ...s };
    });
  }

  const onAutoTagClick = useAutoTagTrigger(
    () => runAutoTag(),
    () => setDialogOpen({ autoTagAlert: true }),
    ui.disableAutoTagWarning
  );

  function renderScanDialog() {
    if (!dialogOpen.scan) {
      return;
    }

    return <DirectorySelectionDialog onClose={onScanDialogClosed} />;
  }

  function onScanDialogClosed(paths?: string[]) {
    if (paths) {
      runScan(paths);
    }

    setDialogOpen({ scan: false });
  }

  async function runScan(paths?: string[]) {
    try {
      await mutateMetadataScan({
        ...scanOptions,
        paths,
      });

      Toast.success(
        intl.formatMessage(
          { id: "config.tasks.added_job_to_queue" },
          { operation_name: intl.formatMessage({ id: "actions.scan" }) }
        )
      );
    } catch (e) {
      Toast.error(e);
    }
  }

  async function runDetectSceneCuts() {
    try {
      await mutateMetadataDetectSceneCuts({});

      Toast.success(
        intl.formatMessage(
          { id: "config.tasks.added_job_to_queue" },
          {
            operation_name: intl.formatMessage({
              id: "actions.detect_scene_cuts",
            }),
          }
        )
      );
    } catch (e) {
      Toast.error(e);
    }
  }

  async function runAnalyzeSceneMetadata() {
    // Re-save the complete selection at launch as well as on each change.
    // This retries any failed or superseded debounced UI-config write.
    persistAnalyzeSceneMetadataOptions(analyzeSceneMetadataOptions);
    try {
      // A previously-persisted true value must not silently be sent once the
      // AI server is unavailable - the checkbox being disabled in the UI is
      // not enough on its own to stop a stale ui.taskDefaults value.
      await mutateMetadataAnalyzeScenes({
        ...analyzeSceneMetadataOptions,
        useLocalAIContext:
          aiAvailable && analyzeSceneMetadataOptions.useLocalAIContext,
        useLocalAIStudioProviderSelection:
          aiAvailable &&
          analyzeSceneMetadataOptions.useLocalAIStudioProviderSelection,
      });

      Toast.success(
        intl.formatMessage(
          { id: "config.tasks.added_job_to_queue" },
          {
            operation_name: intl.formatMessage({
              id: "actions.analyze_scene_metadata",
            }),
          }
        )
      );
    } catch (e) {
      Toast.error(e);
    }
  }

  async function runPurgeSceneMetadataPlans() {
    setPurging(true);
    try {
      const result = await mutatePurgeSceneMetadataPlans(purgeAgeSeconds);
      const count = result.data?.purgeSceneMetadataPlans ?? 0;
      Toast.success(
        intl.formatMessage(
          {
            id: "scene_metadata.review.purged",
            defaultMessage: "Removed {count, number} old analysis runs",
          },
          { count }
        )
      );
    } catch (error) {
      Toast.error(error);
    } finally {
      setPurging(false);
    }
  }

  function renderAutoTagAlert() {
    return (
      <AutoTagConfirmDialog
        show={dialogOpen.autoTagAlert}
        onConfirm={() => {
          setDialogOpen({ autoTagAlert: false });
          runAutoTag();
        }}
        onCancel={() => setDialogOpen({ autoTagAlert: false })}
      />
    );
  }

  function renderAutoTagDialog() {
    if (!dialogOpen.autoTag) {
      return;
    }

    return (
      <DirectorySelectionDialog onClose={onAutoTagDialogClosed}>
        <AutoTagWarning />
      </DirectorySelectionDialog>
    );
  }

  function onAutoTagDialogClosed(paths?: string[]) {
    if (paths) {
      runAutoTag(paths);
    }

    setDialogOpen({ autoTag: false });
  }

  async function runAutoTag(paths?: string[]) {
    try {
      await mutateMetadataAutoTag({
        ...autoTagOptions,
        paths,
      });

      Toast.success(
        intl.formatMessage(
          { id: "config.tasks.added_job_to_queue" },
          { operation_name: intl.formatMessage({ id: "actions.auto_tag" }) }
        )
      );
    } catch (e) {
      Toast.error(e);
    }
  }

  function maybeRenderIdentifyDialog() {
    if (!dialogOpen.identify) return;

    return (
      <IdentifyDialog onClose={() => setDialogOpen({ identify: false })} />
    );
  }

  function renderGenerateDialog() {
    if (!dialogOpen.generate) {
      return;
    }

    return <DirectorySelectionDialog onClose={onGenerateDialogClosed} />;
  }

  function onGenerateDialogClosed(paths?: string[]) {
    if (paths) {
      runGenerate(paths);
    }

    setDialogOpen({ generate: false });
  }

  async function runGenerate(paths?: string[]) {
    const general = configuration?.general;

    try {
      // taskDefaults is an untyped persisted map and can contain fields from
      // older releases. GraphQL rejects unknown input fields with HTTP 400,
      // so copy only the current GenerateMetadataInput contract.
      await mutateMetadataGenerate({
        covers: generateOptions.covers,
        sprites: generateOptions.sprites,
        previews: generateOptions.previews,
        imagePreviews: generateOptions.imagePreviews,
        markers: generateOptions.markers,
        markerImagePreviews: generateOptions.markerImagePreviews,
        markerScreenshots: generateOptions.markerScreenshots,
        transcodes: generateOptions.transcodes,
        forceTranscodes: generateOptions.forceTranscodes,
        phashes: generateOptions.phashes,
        interactiveHeatmapsSpeeds: generateOptions.interactiveHeatmapsSpeeds,
        imagePhashes: generateOptions.imagePhashes,
        imageThumbnails: generateOptions.imageThumbnails,
        clipPreviews: generateOptions.clipPreviews,
        contactSheets: generateOptions.contactSheets,
        subtitles: generateOptions.subtitles,
        subtitleLanguage: generateOptions.subtitleLanguage,
        dubbing: generateOptions.dubbing,
        overwrite: generateOptions.overwrite,
        paths,
        previewOptions: {
          previewSegments:
            general?.previewSegments ??
            generateOptions.previewOptions?.previewSegments,
          previewSegmentDuration:
            general?.previewSegmentDuration ??
            generateOptions.previewOptions?.previewSegmentDuration,
          previewExcludeStart:
            general?.previewExcludeStart ??
            generateOptions.previewOptions?.previewExcludeStart,
          previewExcludeEnd:
            general?.previewExcludeEnd ??
            generateOptions.previewOptions?.previewExcludeEnd,
          previewPreset:
            general?.previewPreset ??
            generateOptions.previewOptions?.previewPreset,
        },
      });

      Toast.success(
        intl.formatMessage(
          { id: "config.tasks.added_job_to_queue" },
          { operation_name: intl.formatMessage({ id: "actions.generate" }) }
        )
      );
    } catch (e) {
      Toast.error(e);
    }
  }

  return (
    <Form.Group>
      {renderScanDialog()}
      {renderAutoTagAlert()}
      {renderAutoTagDialog()}
      {maybeRenderIdentifyDialog()}
      {renderGenerateDialog()}

      <SettingSection headingID="library">
        <SettingGroup
          settingProps={{
            heading: (
              <>
                <FormattedMessage id="actions.scan" />
                <ManualLink tab="Tasks">
                  <Icon icon={faQuestionCircle} />
                </ManualLink>
              </>
            ),
            subHeadingID: "config.tasks.scan_for_content_desc",
          }}
          topLevel={
            <>
              <Button
                variant="secondary"
                type="submit"
                className="mr-2"
                onClick={() => runScan()}
              >
                <FormattedMessage id="actions.scan" />
              </Button>

              <Button
                variant="secondary"
                type="submit"
                className="mr-2"
                onClick={() => setDialogOpen({ scan: true })}
              >
                <FormattedMessage id="actions.selective_scan" />…
              </Button>
            </>
          }
          collapsible
        >
          <ScanOptions options={scanOptions} setOptions={onSetScanOptions} />
        </SettingGroup>
      </SettingSection>

      <SettingSection advanced>
        <Setting
          heading={
            <>
              <FormattedMessage id="config.tasks.identify.heading" />
              <ManualLink tab="Identify">
                <Icon icon={faQuestionCircle} />
              </ManualLink>
            </>
          }
          subHeadingID="config.tasks.identify.description"
        >
          <Button
            variant="secondary"
            type="submit"
            onClick={() => setDialogOpen({ identify: true })}
          >
            <FormattedMessage id="actions.identify" />…
          </Button>
        </Setting>
      </SettingSection>

      <SettingSection advanced>
        <SettingGroup
          settingProps={{
            heading: (
              <>
                <FormattedMessage id="actions.auto_tag" />
                <ManualLink tab="AutoTagging">
                  <Icon icon={faQuestionCircle} />
                </ManualLink>
              </>
            ),
            subHeadingID: "config.tasks.auto_tag_based_on_filenames",
          }}
          topLevel={
            <>
              <Button
                variant="secondary"
                type="submit"
                className="mr-2"
                onClick={onAutoTagClick}
              >
                <FormattedMessage id="actions.auto_tag" />…
              </Button>
              <Button
                variant="secondary"
                type="submit"
                onClick={() => setDialogOpen({ autoTag: true })}
              >
                <FormattedMessage id="actions.selective_auto_tag" />…
              </Button>
            </>
          }
          collapsible
        >
          <AutoTagOptions
            options={autoTagOptions}
            setOptions={onSetAutoTagOptions}
          />
          <BooleanSetting
            id="disable_auto_tag_warning"
            headingID="config.tasks.auto_tag.disable_warning.heading"
            subHeadingID="config.tasks.auto_tag.disable_warning.description"
            checked={ui.disableAutoTagWarning ?? undefined}
            onChange={(v) => saveUI({ disableAutoTagWarning: v })}
          />
        </SettingGroup>
      </SettingSection>

      <SettingSection advanced>
        <SceneMetadataModelsPanel />
        <Setting
          heading={<FormattedMessage id="actions.analyze_scene_metadata" />}
          subHeadingID="config.tasks.analyze_scene_metadata.description"
        >
          <Form.Group controlId="analyze-scene-metadata-verifier-scrapers">
            <Form.Label>
              <FormattedMessage id="config.tasks.analyze_scene_metadata.performer_verifier_scrapers.label" />
            </Form.Label>
            <SelectComponent
              items={performerVerifierOptions}
              selectedOptions={performerVerifierOptions.filter((option) =>
                analyzeSceneMetadataOptions.performerVerifierScraperIDs.includes(
                  option.value
                )
              )}
              isLoading={performerScrapersLoading}
              isMulti
              closeMenuOnSelect={false}
              onChange={(selected) =>
                onSetAnalyzeSceneMetadataOptions({
                  performerVerifierScraperIDs: selected.map(
                    (option) => option.value
                  ),
                })
              }
              className="form-control react-select"
              placeholder={intl.formatMessage({
                id: "config.tasks.analyze_scene_metadata.performer_verifier_scrapers.placeholder",
              })}
            />
            <Form.Text className="text-muted">
              <FormattedMessage id="config.tasks.analyze_scene_metadata.performer_verifier_scrapers.help" />
            </Form.Text>
          </Form.Group>
          <Form.Group controlId="analyze-scene-metadata-verifier-stash-boxes">
            <Form.Label>
              <FormattedMessage id="config.tasks.analyze_scene_metadata.performer_verifier_stash_boxes.label" />
            </Form.Label>
            <SelectComponent
              items={stashBoxVerifierOptions}
              selectedOptions={stashBoxVerifierOptions.filter((option) =>
                analyzeSceneMetadataOptions.performerVerifierStashBoxEndpoints.includes(
                  option.value
                )
              )}
              isLoading={false}
              isMulti
              closeMenuOnSelect={false}
              onChange={(selected) =>
                onSetAnalyzeSceneMetadataOptions({
                  performerVerifierStashBoxEndpoints: selected.map(
                    (option) => option.value
                  ),
                })
              }
              className="form-control react-select"
              placeholder={intl.formatMessage({
                id: "config.tasks.analyze_scene_metadata.performer_verifier_stash_boxes.placeholder",
              })}
            />
            <Form.Text className="text-muted">
              <FormattedMessage id="config.tasks.analyze_scene_metadata.performer_verifier_stash_boxes.help" />
            </Form.Text>
          </Form.Group>
          <div className="row">
            <Form.Group
              className="col-md-6"
              controlId="analyze-scene-metadata-performer-confidence"
            >
              <Form.Label>
                <FormattedMessage id="config.tasks.analyze_scene_metadata.performer_confidence.label" />
              </Form.Label>
              <Form.Control
                type="number"
                min={0.3}
                max={1}
                step={0.05}
                value={analyzeSceneMetadataOptions.performerConfidenceThreshold}
                onChange={(event) =>
                  onSetAnalyzeSceneMetadataOptions({
                    performerConfidenceThreshold: Number(
                      event.currentTarget.value
                    ),
                  })
                }
              />
              <Form.Text className="text-muted">
                <FormattedMessage id="config.tasks.analyze_scene_metadata.performer_confidence.note" />
              </Form.Text>
            </Form.Group>
            <Form.Group
              className="col-md-6"
              controlId="analyze-scene-metadata-date-confidence"
            >
              <Form.Label>
                <FormattedMessage id="config.tasks.analyze_scene_metadata.date_confidence.label" />
              </Form.Label>
              <Form.Control
                type="number"
                min={0}
                max={1}
                step={0.05}
                value={analyzeSceneMetadataOptions.dateConfidenceThreshold}
                onChange={(event) =>
                  onSetAnalyzeSceneMetadataOptions({
                    dateConfidenceThreshold: Number(event.currentTarget.value),
                  })
                }
              />
            </Form.Group>
          </div>
          <Form.Group controlId="analyze-scene-metadata-studio-verifier-scrapers">
            <Form.Label>
              <FormattedMessage id="config.tasks.analyze_scene_metadata.studio_verifier_scrapers.label" />
            </Form.Label>
            <SelectComponent
              items={studioVerifierScraperOptions}
              selectedOptions={studioVerifierScraperOptions.filter((option) =>
                analyzeSceneMetadataOptions.studioVerifierScraperIDs.includes(
                  option.value
                )
              )}
              isLoading={studioScrapersLoading}
              isMulti
              closeMenuOnSelect={false}
              onChange={(selected) =>
                onSetAnalyzeSceneMetadataOptions({
                  studioVerifierScraperIDs: selected.map(
                    (option) => option.value
                  ),
                })
              }
              className="form-control react-select"
              placeholder={intl.formatMessage({
                id: "config.tasks.analyze_scene_metadata.studio_verifier_scrapers.placeholder",
              })}
            />
            <Form.Text className="text-muted">
              <FormattedMessage id="config.tasks.analyze_scene_metadata.studio_verifier_scrapers.help" />
            </Form.Text>
          </Form.Group>
          <Form.Group controlId="analyze-scene-metadata-studio-verifier-stash-boxes">
            <Form.Label>
              <FormattedMessage id="config.tasks.analyze_scene_metadata.studio_verifier_stash_boxes.label" />
            </Form.Label>
            <SelectComponent
              items={stashBoxVerifierOptions}
              selectedOptions={stashBoxVerifierOptions.filter((option) =>
                analyzeSceneMetadataOptions.studioVerifierStashBoxEndpoints.includes(
                  option.value
                )
              )}
              isLoading={false}
              isMulti
              closeMenuOnSelect={false}
              onChange={(selected) =>
                onSetAnalyzeSceneMetadataOptions({
                  studioVerifierStashBoxEndpoints: selected.map(
                    (option) => option.value
                  ),
                })
              }
              className="form-control react-select"
              placeholder={intl.formatMessage({
                id: "config.tasks.analyze_scene_metadata.studio_verifier_stash_boxes.placeholder",
              })}
            />
            <Form.Text className="text-muted">
              <FormattedMessage id="config.tasks.analyze_scene_metadata.studio_verifier_stash_boxes.help" />
            </Form.Text>
          </Form.Group>
          <Form.Group controlId="analyze-scene-metadata-provider-policies">
            <Form.Label>
              <FormattedMessage id="config.tasks.analyze_scene_metadata.provider_policies.label" />
            </Form.Label>
            <Form.Text className="text-muted d-block mb-2">
              <FormattedMessage id="config.tasks.analyze_scene_metadata.provider_policies.help" />
            </Form.Text>
            {analyzeSceneMetadataOptions.providerPolicies.map((policy) => (
              <div
                key={policy.endpoint}
                className="border rounded p-2 mb-2"
                draggable
                onDragStart={(event) =>
                  event.dataTransfer.setData("text/plain", policy.endpoint)
                }
                onDragOver={(event) => event.preventDefault()}
                onDrop={(event) => {
                  event.preventDefault();
                  moveProviderPolicy(
                    event.dataTransfer.getData("text/plain"),
                    policy.endpoint
                  );
                }}
              >
                <div className="font-weight-bold mb-2">
                  {stashBoxVerifierOptions.find(
                    (option) => option.value === policy.endpoint
                  )?.label ?? policy.endpoint}
                </div>
                <div className="row">
                  {(
                    [
                      [
                        "performerMode",
                        "config.tasks.analyze_scene_metadata.provider_policies.performers",
                      ],
                      [
                        "studioMode",
                        "config.tasks.analyze_scene_metadata.provider_policies.studio",
                      ],
                      [
                        "dateMode",
                        "config.tasks.analyze_scene_metadata.provider_policies.date",
                      ],
                      [
                        "titleMode",
                        "config.tasks.analyze_scene_metadata.provider_policies.title",
                      ],
                    ] as const
                  ).map(([field, label]) => (
                    <Form.Group className="col-sm-6 col-lg-3 mb-1" key={field}>
                      <Form.Label className="small mb-1">
                        <FormattedMessage id={label} />
                      </Form.Label>
                      <Form.Control
                        as="select"
                        size="sm"
                        value={policy[field]}
                        onChange={(event) =>
                          updateProviderPolicy(policy.endpoint, {
                            [field]: event.currentTarget
                              .value as GQL.SceneMetadataProviderFieldMode,
                          })
                        }
                      >
                        <option
                          value={GQL.SceneMetadataProviderFieldMode.Observe}
                        >
                          {intl.formatMessage({
                            id: "config.tasks.analyze_scene_metadata.provider_policies.observe",
                          })}
                        </option>
                        <option
                          value={GQL.SceneMetadataProviderFieldMode.Merge}
                        >
                          {intl.formatMessage({
                            id: "config.tasks.analyze_scene_metadata.provider_policies.merge",
                          })}
                        </option>
                        <option
                          value={GQL.SceneMetadataProviderFieldMode.Replace}
                        >
                          {intl.formatMessage({
                            id: "config.tasks.analyze_scene_metadata.provider_policies.replace",
                          })}
                        </option>
                      </Form.Control>
                    </Form.Group>
                  ))}
                </div>
              </div>
            ))}
          </Form.Group>
          <Form.Check
            id="analyze-scene-metadata-replace-local-performers"
            checked={
              analyzeSceneMetadataOptions.replaceLocalPerformersFromRemote
            }
            label={intl.formatMessage({
              id: "config.tasks.analyze_scene_metadata.provider_policies.replace_performers",
            })}
            onChange={() =>
              onSetAnalyzeSceneMetadataOptions({
                replaceLocalPerformersFromRemote:
                  !analyzeSceneMetadataOptions.replaceLocalPerformersFromRemote,
              })
            }
            className="mb-2"
          />
          <Form.Check
            id="analyze-scene-metadata-use-local-ai-studio-provider"
            checked={
              analyzeSceneMetadataOptions.useLocalAIStudioProviderSelection
            }
            disabled={!aiAvailable}
            label={intl.formatMessage({
              id: "config.tasks.analyze_scene_metadata.use_local_ai_studio_provider_selection",
            })}
            onChange={() =>
              onSetAnalyzeSceneMetadataOptions({
                useLocalAIStudioProviderSelection:
                  !analyzeSceneMetadataOptions.useLocalAIStudioProviderSelection,
              })
            }
            className="mb-2"
          />
          {!aiAvailable && (
            <Form.Text className="text-muted">
              <FormattedMessage id="config.tasks.analyze_scene_metadata.use_local_ai_studio_provider_selection_unavailable_help" />
            </Form.Text>
          )}
          <Form.Check
            id="analyze-scene-metadata-use-details"
            checked={analyzeSceneMetadataOptions.useDetails}
            label={intl.formatMessage({
              id: "config.tasks.analyze_scene_metadata.use_details",
            })}
            onChange={() =>
              onSetAnalyzeSceneMetadataOptions({
                useDetails: !analyzeSceneMetadataOptions.useDetails,
              })
            }
            className="mb-2"
          />
          <Form.Check
            id="analyze-scene-metadata-use-local-ai-context"
            checked={analyzeSceneMetadataOptions.useLocalAIContext}
            disabled={!aiAvailable}
            label={intl.formatMessage({
              id: "config.tasks.analyze_scene_metadata.use_local_ai_context",
            })}
            onChange={() =>
              onSetAnalyzeSceneMetadataOptions({
                useLocalAIContext:
                  !analyzeSceneMetadataOptions.useLocalAIContext,
              })
            }
            className="mb-2"
          />
          {!aiAvailable && (
            <Form.Text className="text-muted">
              <FormattedMessage id="config.tasks.analyze_scene_metadata.use_local_ai_context_unavailable_help" />
            </Form.Text>
          )}
          <Form.Check
            id="analyze-scene-metadata-overwrite-date"
            checked={analyzeSceneMetadataOptions.overwriteExistingDate}
            label={intl.formatMessage({
              id: "config.tasks.analyze_scene_metadata.overwrite_date",
            })}
            onChange={() =>
              onSetAnalyzeSceneMetadataOptions({
                overwriteExistingDate:
                  !analyzeSceneMetadataOptions.overwriteExistingDate,
              })
            }
            className="mb-2"
          />
          <Form.Check
            id="analyze-scene-metadata-overwrite-title"
            checked={analyzeSceneMetadataOptions.overwriteExistingTitle}
            label={intl.formatMessage({
              id: "config.tasks.analyze_scene_metadata.overwrite_title",
            })}
            onChange={() =>
              onSetAnalyzeSceneMetadataOptions({
                overwriteExistingTitle:
                  !analyzeSceneMetadataOptions.overwriteExistingTitle,
              })
            }
            className="mb-2"
          />
          <Form.Check
            id="analyze-scene-metadata-dry-run"
            checked={analyzeSceneMetadataOptions.dryRun}
            label={intl.formatMessage({ id: "config.tasks.dry_run" })}
            onChange={() =>
              onSetAnalyzeSceneMetadataOptions({
                dryRun: !analyzeSceneMetadataOptions.dryRun,
              })
            }
            className="mb-2"
          />
          <Button
            variant="secondary"
            type="submit"
            onClick={runAnalyzeSceneMetadata}
            disabled={!analyzeSceneMetadataOptionsInitialized}
          >
            <FormattedMessage id="actions.analyze_scene_metadata" />…
          </Button>
          <Button
            variant="secondary"
            type="button"
            className="ml-2"
            onClick={() => setShowReview(true)}
          >
            <FormattedMessage
              id="scene_metadata.review.action"
              defaultMessage="Review proposals"
            />
          </Button>
          <div className="d-flex flex-wrap align-items-end mt-3">
            <Form.Group
              className="mb-0 mr-2"
              controlId="purge-scene-metadata-plans"
            >
              <Form.Label>
                <FormattedMessage
                  id="scene_metadata.review.purge_age"
                  defaultMessage="Remove applied runs older than"
                />
              </Form.Label>
              <Form.Control
                as="select"
                className="input-control"
                value={purgeAgeSeconds}
                onChange={(event) =>
                  setPurgeAgeSeconds(Number(event.currentTarget.value))
                }
              >
                <option value={3600}>
                  {intl.formatMessage({
                    id: "scene_metadata.review.purge_hour",
                    defaultMessage: "1 hour",
                  })}
                </option>
                <option value={86400}>
                  {intl.formatMessage({
                    id: "scene_metadata.review.purge_day",
                    defaultMessage: "1 day",
                  })}
                </option>
                <option value={604800}>
                  {intl.formatMessage({
                    id: "scene_metadata.review.purge_week",
                    defaultMessage: "7 days",
                  })}
                </option>
                <option value={2592000}>
                  {intl.formatMessage({
                    id: "scene_metadata.review.purge_month",
                    defaultMessage: "30 days",
                  })}
                </option>
              </Form.Control>
            </Form.Group>
            <Button
              variant="danger"
              type="button"
              disabled={purging}
              onClick={() => void runPurgeSceneMetadataPlans()}
            >
              <FormattedMessage
                id="scene_metadata.review.purge"
                defaultMessage="Remove old runs"
              />
            </Button>
          </div>
        </Setting>
      </SettingSection>

      <SettingSection advanced>
        <Setting
          heading={<FormattedMessage id="actions.detect_scene_cuts" />}
          subHeadingID="config.tasks.detect_scene_cuts.description"
        >
          <Button
            variant="secondary"
            type="submit"
            onClick={runDetectSceneCuts}
          >
            <FormattedMessage id="actions.detect_scene_cuts" />…
          </Button>
        </Setting>
      </SettingSection>

      <SettingSection headingID="config.tasks.generated_content">
        <SettingGroup
          settingProps={{
            heading: (
              <>
                <FormattedMessage id="actions.generate" />
                <ManualLink tab="Tasks">
                  <Icon icon={faQuestionCircle} />
                </ManualLink>
              </>
            ),
            subHeadingID: "config.tasks.generate_desc",
          }}
          topLevel={
            <>
              <Button
                variant="secondary"
                type="submit"
                onClick={() => runGenerate()}
              >
                <FormattedMessage id="actions.generate" />
              </Button>
              <Button
                variant="secondary"
                type="submit"
                className="mr-2"
                onClick={() => setDialogOpen({ generate: true })}
              >
                <FormattedMessage id="actions.selective_generate" />…
              </Button>
            </>
          }
          collapsible
        >
          <GenerateOptions
            options={generateOptions}
            setOptions={onSetGenerateOptions}
          />
        </SettingGroup>
      </SettingSection>
      <SceneMetadataPlanReviewModal
        show={showReview}
        sceneIds={[]}
        onHide={() => setShowReview(false)}
      />
    </Form.Group>
  );
};
