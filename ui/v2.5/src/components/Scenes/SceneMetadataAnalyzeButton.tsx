import React, { useMemo, useState } from "react";
import { Button, Form } from "react-bootstrap";
import { faSearch } from "@fortawesome/free-solid-svg-icons";
import { FormattedMessage, useIntl } from "react-intl";
import * as GQL from "src/core/generated-graphql";
import {
  mutateMetadataAnalyzeScenes,
  useConfiguration,
} from "src/core/StashService";
import { useToast } from "src/hooks/Toast";
import { ModalComponent } from "src/components/Shared/Modal";

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
  const [show, setShow] = useState(false);
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
      await mutateMetadataAnalyzeScenes({ ...options, sceneIDs: sceneIds });
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
          </Form>
        </ModalComponent>
      )}
    </>
  );
};
