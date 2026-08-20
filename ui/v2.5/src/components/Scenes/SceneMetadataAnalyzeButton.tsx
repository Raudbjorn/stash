import React, { useMemo, useState } from "react";
import { Button, Form } from "react-bootstrap";
import { faSearch } from "@fortawesome/free-solid-svg-icons";
import { FormattedMessage, useIntl } from "react-intl";
import * as GQL from "src/core/generated-graphql";
import {
  mutateMetadataAnalyzeScenes,
  useAIServerAvailability,
  useConfiguration,
  useListPerformerScrapers,
  useListStudioScrapers,
} from "src/core/StashService";
import { useToast } from "src/hooks/Toast";
import { ModalComponent } from "src/components/Shared/Modal";
import { SceneMetadataPlanReviewModal } from "./SceneMetadataPlanReviewModal";
import { SelectComponent } from "src/components/Shared/Select";

const builtinStudioURLScraperID = "builtin-studio-url-map";

interface ISceneMetadataAnalyzeButton {
  sceneIds: string[];
  className?: string;
  variant?: string;
}

function defaultOptions(): GQL.AnalyzeSceneMetadataInput {
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
  };
}

export const SceneMetadataAnalyzeButton: React.FC<
  ISceneMetadataAnalyzeButton
> = ({ sceneIds, className, variant = "secondary" }) => {
  const intl = useIntl();
  const Toast = useToast();
  const { data: configurationData } = useConfiguration();
  const { data: performerScrapersData, loading: performerScrapersLoading } =
    useListPerformerScrapers();
  const { data: studioScrapersData, loading: studioScrapersLoading } =
    useListStudioScrapers();
  const { data: aiServerData } = useAIServerAvailability();
  const aiAvailable = !!(
    aiServerData?.aiServerStatus.hasVLMProvider ||
    aiServerData?.aiServerStatus.hasLocalTextProvider
  );
  const performerVerifierOptions = useMemo(
    () =>
      (performerScrapersData?.listScrapers ?? [])
        .filter((scraper) =>
          scraper.performer?.supported_scrapes.includes(GQL.ScrapeType.Name)
        )
        .map((scraper) => ({ label: scraper.name, value: scraper.id })),
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
        .filter((scraper) =>
          scraper.studio?.supported_scrapes.includes(GQL.ScrapeType.Name)
        )
        .map((scraper) => ({ label: scraper.name, value: scraper.id })),
    ],
    [intl, studioScrapersData]
  );
  const stashBoxVerifierOptions = useMemo(
    () =>
      (configurationData?.configuration.general.stashBoxes ?? []).map(
        (box) => ({
          label: box.name ? `${box.name} — ${box.endpoint}` : box.endpoint,
          value: box.endpoint,
        })
      ),
    [configurationData]
  );
  const [show, setShow] = useState(false);
  const [showReview, setShowReview] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const persisted = (
    configurationData?.configuration.ui as
      | {
          taskDefaults?: {
            analyzeSceneMetadata?: Partial<GQL.AnalyzeSceneMetadataInput>;
          };
        }
      | undefined
  )?.taskDefaults?.analyzeSceneMetadata;
  const initialOptions = useMemo(
    () => ({ ...defaultOptions(), ...persisted, sceneIDs: sceneIds }),
    [persisted, sceneIds]
  );
  const [options, setOptions] =
    useState<GQL.AnalyzeSceneMetadataInput>(initialOptions);

  function open() {
    setOptions(initialOptions);
    setShow(true);
  }

  function setOption(partial: Partial<GQL.AnalyzeSceneMetadataInput>) {
    setOptions((current) => ({ ...current, ...partial }));
  }

  async function analyze() {
    setSubmitting(true);
    try {
      await mutateMetadataAnalyzeScenes({
        ...options,
        sceneIDs: sceneIds,
        useLocalAIContext: aiAvailable && (options.useLocalAIContext ?? false),
        useLocalAIStudioProviderSelection:
          aiAvailable && (options.useLocalAIStudioProviderSelection ?? false),
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
      setShow(false);
      if (options.dryRun ?? true) setShowReview(true);
    } catch (error) {
      Toast.error(error);
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <>
      <Button
        variant={variant}
        className={className}
        disabled={sceneIds.length === 0}
        onClick={open}
      >
        <FormattedMessage id="actions.analyze_scene_metadata" />…
      </Button>
      <Button
        variant="secondary"
        className={className ? `${className} ml-2` : "ml-2"}
        disabled={sceneIds.length === 0}
        onClick={() => setShowReview(true)}
      >
        <FormattedMessage
          id="scene_metadata.review.action"
          defaultMessage="Review proposals"
        />
      </Button>
      {show && (
        <ModalComponent
          show
          modalProps={{ size: "lg" }}
          icon={faSearch}
          header={intl.formatMessage({ id: "actions.analyze_scene_metadata" })}
          accept={{
            onClick: () => {
              if (!submitting) void analyze();
            },
            text: intl.formatMessage({ id: "actions.analyze_scene_metadata" }),
          }}
          cancel={{
            onClick: () => setShow(false),
            text: intl.formatMessage({ id: "actions.cancel" }),
            variant: "secondary",
          }}
        >
          <Form>
            <p>
              <FormattedMessage
                id="config.tasks.analyze_scene_metadata.selection_count"
                values={{ count: sceneIds.length }}
              />
            </p>
            <div className="row">
              <Form.Group className="col-md-6">
                <Form.Label>
                  <FormattedMessage id="config.tasks.analyze_scene_metadata.performer_confidence.label" />
                </Form.Label>
                <Form.Control
                  type="number"
                  min={0.3}
                  max={1}
                  step={0.05}
                  value={options.performerConfidenceThreshold ?? 0.6}
                  onChange={(event) =>
                    setOption({
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
              <Form.Group className="col-md-6">
                <Form.Label>
                  <FormattedMessage id="config.tasks.analyze_scene_metadata.date_confidence.label" />
                </Form.Label>
                <Form.Control
                  type="number"
                  min={0}
                  max={1}
                  step={0.05}
                  value={options.dateConfidenceThreshold ?? 0.6}
                  onChange={(event) =>
                    setOption({
                      dateConfidenceThreshold: Number(
                        event.currentTarget.value
                      ),
                    })
                  }
                />
              </Form.Group>
            </div>
            <Form.Check
              id={`scene-metadata-dry-run-${sceneIds.join("-")}`}
              checked={options.dryRun ?? true}
              label={intl.formatMessage({ id: "config.tasks.dry_run" })}
              onChange={() => setOption({ dryRun: !(options.dryRun ?? true) })}
              className="mb-2"
            />
            <Form.Check
              id={`scene-metadata-details-${sceneIds.join("-")}`}
              checked={options.useDetails ?? false}
              label={intl.formatMessage({
                id: "config.tasks.analyze_scene_metadata.use_details",
              })}
              onChange={() =>
                setOption({ useDetails: !(options.useDetails ?? false) })
              }
              className="mb-2"
            />
            <Form.Check
              id={`scene-metadata-overwrite-date-${sceneIds.join("-")}`}
              checked={options.overwriteExistingDate ?? false}
              label={intl.formatMessage({
                id: "config.tasks.analyze_scene_metadata.overwrite_date",
              })}
              onChange={() =>
                setOption({
                  overwriteExistingDate: !(
                    options.overwriteExistingDate ?? false
                  ),
                })
              }
              className="mb-2"
            />
            <Form.Check
              id={`scene-metadata-overwrite-title-${sceneIds.join("-")}`}
              checked={options.overwriteExistingTitle ?? false}
              label={intl.formatMessage({
                id: "config.tasks.analyze_scene_metadata.overwrite_title",
              })}
              onChange={() =>
                setOption({
                  overwriteExistingTitle: !(
                    options.overwriteExistingTitle ?? false
                  ),
                })
              }
              className="mb-2"
            />
            <Form.Check
              id={`scene-metadata-replace-performers-${sceneIds.join("-")}`}
              checked={options.replaceLocalPerformersFromRemote ?? false}
              label={intl.formatMessage({
                id: "config.tasks.analyze_scene_metadata.provider_policies.replace_performers",
              })}
              onChange={() =>
                setOption({
                  replaceLocalPerformersFromRemote: !(
                    options.replaceLocalPerformersFromRemote ?? false
                  ),
                })
              }
            />
            <details className="mt-3">
              <summary>
                {intl.formatMessage({
                  id: "scene_metadata.review.advanced_options",
                  defaultMessage: "Scrapers, Stash-box, studio, and local AI",
                })}
              </summary>
              <Form.Group className="mt-2">
                <Form.Label>
                  <FormattedMessage id="config.tasks.analyze_scene_metadata.performer_verifier_scrapers.label" />
                </Form.Label>
                <SelectComponent
                  items={performerVerifierOptions}
                  selectedOptions={performerVerifierOptions.filter((option) =>
                    (options.performerVerifierScraperIDs ?? []).includes(
                      option.value
                    )
                  )}
                  isLoading={performerScrapersLoading}
                  isMulti
                  closeMenuOnSelect={false}
                  onChange={(selected) =>
                    setOption({
                      performerVerifierScraperIDs: selected.map(
                        (option) => option.value
                      ),
                    })
                  }
                />
              </Form.Group>
              <Form.Group>
                <Form.Label>
                  <FormattedMessage id="config.tasks.analyze_scene_metadata.performer_verifier_stash_boxes.label" />
                </Form.Label>
                <SelectComponent
                  items={stashBoxVerifierOptions}
                  selectedOptions={stashBoxVerifierOptions.filter((option) =>
                    (options.performerVerifierStashBoxEndpoints ?? []).includes(
                      option.value
                    )
                  )}
                  isLoading={false}
                  isMulti
                  closeMenuOnSelect={false}
                  onChange={(selected) => {
                    const endpoints = selected.map((option) => option.value);
                    const existing = options.providerPolicies ?? [];
                    setOption({
                      performerVerifierStashBoxEndpoints: endpoints,
                      providerPolicies: endpoints.map((endpoint, priority) => {
                        const current = existing.find(
                          (policy) => policy.endpoint === endpoint
                        );
                        return (
                          current ?? {
                            endpoint,
                            priority,
                            performerMode:
                              GQL.SceneMetadataProviderFieldMode.Observe,
                            studioMode:
                              GQL.SceneMetadataProviderFieldMode.Observe,
                            dateMode:
                              GQL.SceneMetadataProviderFieldMode.Observe,
                            titleMode:
                              GQL.SceneMetadataProviderFieldMode.Observe,
                          }
                        );
                      }),
                    });
                  }}
                />
              </Form.Group>
              <Form.Group>
                <Form.Label>
                  <FormattedMessage id="config.tasks.analyze_scene_metadata.studio_verifier_scrapers.label" />
                </Form.Label>
                <SelectComponent
                  items={studioVerifierScraperOptions}
                  selectedOptions={studioVerifierScraperOptions.filter(
                    (option) =>
                      (options.studioVerifierScraperIDs ?? []).includes(
                        option.value
                      )
                  )}
                  isLoading={studioScrapersLoading}
                  isMulti
                  closeMenuOnSelect={false}
                  onChange={(selected) =>
                    setOption({
                      studioVerifierScraperIDs: selected.map(
                        (option) => option.value
                      ),
                    })
                  }
                />
              </Form.Group>
              <Form.Group>
                <Form.Label>
                  <FormattedMessage id="config.tasks.analyze_scene_metadata.studio_verifier_stash_boxes.label" />
                </Form.Label>
                <SelectComponent
                  items={stashBoxVerifierOptions}
                  selectedOptions={stashBoxVerifierOptions.filter((option) =>
                    (options.studioVerifierStashBoxEndpoints ?? []).includes(
                      option.value
                    )
                  )}
                  isLoading={false}
                  isMulti
                  closeMenuOnSelect={false}
                  onChange={(selected) =>
                    setOption({
                      studioVerifierStashBoxEndpoints: selected.map(
                        (option) => option.value
                      ),
                    })
                  }
                />
              </Form.Group>
              {(options.providerPolicies ?? []).map((policy) => (
                <div key={policy.endpoint} className="border rounded p-2 mb-2">
                  <div className="small font-weight-bold mb-2">
                    {policy.endpoint}
                  </div>
                  {(
                    [
                      ["performerMode", "performers"],
                      ["studioMode", "studio"],
                      ["dateMode", "date"],
                      ["titleMode", "title"],
                    ] as const
                  ).map(([field, label]) => (
                    <Form.Group key={field} className="mb-1">
                      <Form.Label className="small mb-0">
                        <FormattedMessage
                          id={`config.tasks.analyze_scene_metadata.provider_policies.${label}`}
                        />
                      </Form.Label>
                      <Form.Control
                        as="select"
                        className="input-control"
                        value={policy[field]}
                        onChange={(event) =>
                          setOption({
                            providerPolicies: (
                              options.providerPolicies ?? []
                            ).map((item) =>
                              item.endpoint === policy.endpoint
                                ? {
                                    ...item,
                                    [field]: event.currentTarget
                                      .value as GQL.SceneMetadataProviderFieldMode,
                                  }
                                : item
                            ),
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
              ))}
              <Form.Check
                id={`scene-metadata-local-ai-context-${sceneIds.join("-")}`}
                checked={options.useLocalAIContext ?? false}
                disabled={!aiAvailable}
                label={intl.formatMessage({
                  id: "config.tasks.analyze_scene_metadata.use_local_ai_context",
                })}
                onChange={() =>
                  setOption({
                    useLocalAIContext: !(options.useLocalAIContext ?? false),
                  })
                }
                className="mb-2"
              />
              <Form.Check
                id={`scene-metadata-local-ai-studio-${sceneIds.join("-")}`}
                checked={options.useLocalAIStudioProviderSelection ?? false}
                disabled={!aiAvailable}
                label={intl.formatMessage({
                  id: "config.tasks.analyze_scene_metadata.use_local_ai_studio_provider_selection",
                })}
                onChange={() =>
                  setOption({
                    useLocalAIStudioProviderSelection: !(
                      options.useLocalAIStudioProviderSelection ?? false
                    ),
                  })
                }
              />
            </details>
          </Form>
        </ModalComponent>
      )}
      <SceneMetadataPlanReviewModal
        show={showReview}
        sceneIds={sceneIds}
        onHide={() => setShowReview(false)}
      />
    </>
  );
};
