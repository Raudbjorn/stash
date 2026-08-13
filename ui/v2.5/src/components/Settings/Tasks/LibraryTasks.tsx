import React, { useEffect, useMemo, useState } from "react";
import { FormattedMessage, useIntl } from "react-intl";
import { Button, Form } from "react-bootstrap";
import {
  mutateMetadataScan,
  mutateMetadataAutoTag,
  mutateMetadataGenerate,
  mutateMetadataDetectSceneCuts,
  mutateMetadataAnalyzeScenes,
  useListPerformerScrapers,
  useListStudioScrapers,
  mutateSceneMetadataEntityModelInstall,
  mutateSceneMetadataEntityModelReload,
  useSceneMetadataEntityModelStatus,
  useAIServerAvailability,
} from "src/core/StashService";
import { withoutTypename } from "src/utils/data";
import { useConfigurationContext } from "src/hooks/Config";
import { useAutoTagTrigger } from "src/hooks/useAutoTagTrigger";
import { IdentifyDialog } from "../../Dialogs/IdentifyDialog/IdentifyDialog";
import * as GQL from "src/core/generated-graphql";
import { DirectorySelectionDialog } from "./DirectorySelectionDialog";
import { ScanOptions } from "./ScanOptions";
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

export const LibraryTasks: React.FC = () => {
  const intl = useIntl();
  const Toast = useToast();
  const { ui, saveUI, loading } = useSettings();

  const { taskDefaults } = ui;

  const { data: aiServerData } = useAIServerAvailability();
  const aiAvailable = !!(
    aiServerData?.aiServerStatus.enabled &&
    aiServerData?.aiServerStatus.hasVLMProvider
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
  const {
    data: entityModelStatusData,
    loading: entityModelStatusLoading,
    refetch: refetchEntityModelStatus,
  } = useSceneMetadataEntityModelStatus();
  const { configuration } = useConfigurationContext();
  const entityModelStatus =
    entityModelStatusData?.sceneMetadataEntityModelStatus;
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
    () =>
      (studioScrapersData?.listScrapers ?? [])
        .filter((s) =>
          s.studio?.supported_scrapes.includes(GQL.ScrapeType.Name)
        )
        .map((s) => ({ label: s.name, value: s.id })),
    [studioScrapersData]
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
      reconciledStashBoxEndpoints.length !== requestedStashBoxEndpoints.length ||
      reconciledStudioIDs.length !== requestedStudioIDs.length ||
      reconciledStudioStashBoxEndpoints.length !==
        requestedStudioStashBoxEndpoints.length
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

  function onSetAnalyzeSceneMetadataOptions(
    partial: Partial<IAnalyzeSceneMetadataTaskDefaults>
  ) {
    const nextOptions = { ...analyzeSceneMetadataOptions, ...partial };
    configureDefaults({ analyzeSceneMetadata: nextOptions });
    setAnalyzeSceneMetadataOptions(nextOptions);
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
  async function installSceneMetadataEntityModel() {
    try {
      await mutateSceneMetadataEntityModelInstall();
      Toast.success(
        intl.formatMessage(
          { id: "config.tasks.added_job_to_queue" },
          {
            operation_name: intl.formatMessage(
              {
                id: "config.tasks.analyze_scene_metadata.model.install",
              },
              { model: entityModelStatus?.modelID ?? "entity model" }
            ),
          }
        )
      );
      await refetchEntityModelStatus();
    } catch (e) {
      Toast.error(e);
    }
  }

  async function reloadSceneMetadataEntityModel() {
    try {
      await mutateSceneMetadataEntityModelReload();
      await refetchEntityModelStatus();
      Toast.success(
        intl.formatMessage({
          id: "config.tasks.analyze_scene_metadata.model.reloaded",
        })
      );
    } catch (e) {
      Toast.error(e);
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
      await mutateMetadataGenerate({
        ...generateOptions,
        paths,
        previewOptions: {
          ...generateOptions.previewOptions,
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
        <Setting
          heading={<FormattedMessage id="actions.analyze_scene_metadata" />}
          subHeadingID="config.tasks.analyze_scene_metadata.description"
        >
          <div className="border rounded p-3 mb-3" aria-live="polite">
            <Form.Label className="mb-1">
              <FormattedMessage id="config.tasks.analyze_scene_metadata.model.label" />
            </Form.Label>
            <div className="text-muted small mb-2">
              {entityModelStatusLoading ? (
                <FormattedMessage id="config.tasks.analyze_scene_metadata.model.checking" />
              ) : (
                <FormattedMessage
                  id="config.tasks.analyze_scene_metadata.model.status"
                  values={{
                    state: entityModelStatus?.state ?? "unknown",
                    version: entityModelStatus?.version ?? "unknown",
                    runtime: entityModelStatus?.runtimeAvailable
                      ? intl.formatMessage({
                          id: "config.tasks.analyze_scene_metadata.model.runtime_available",
                        })
                      : intl.formatMessage({
                          id: "config.tasks.analyze_scene_metadata.model.runtime_missing",
                        }),
                  }}
                />
              )}
            </div>
            {entityModelStatus?.lastError ? (
              <div className="text-danger small mb-2">
                {entityModelStatus.lastError}
              </div>
            ) : null}
            <Button
              variant="secondary"
              size="sm"
              type="button"
              className="mr-2"
              onClick={installSceneMetadataEntityModel}
              disabled={
                entityModelStatusLoading ||
                !entityModelStatus?.runtimeAvailable ||
                entityModelStatus?.state === "loading"
              }
            >
              <FormattedMessage
                id="config.tasks.analyze_scene_metadata.model.install"
                values={{ model: entityModelStatus?.modelID ?? "entity model" }}
              />
            </Button>
            <Button
              variant="secondary"
              size="sm"
              type="button"
              onClick={reloadSceneMetadataEntityModel}
              disabled={
                entityModelStatusLoading ||
                !entityModelStatus?.runtimeAvailable ||
                entityModelStatus?.state !== "ready"
              }
            >
              <FormattedMessage id="config.tasks.analyze_scene_metadata.model.reload" />
            </Button>
          </div>

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
    </Form.Group>
  );
};
