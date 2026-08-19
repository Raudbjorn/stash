import React, { useMemo, useState } from "react";
import { Alert, Badge, Button, Spinner } from "react-bootstrap";
import { faClipboardCheck } from "@fortawesome/free-solid-svg-icons";
import { useIntl } from "react-intl";
import { Link } from "react-router-dom";
import * as GQL from "src/core/generated-graphql";
import {
  mutateAcceptSceneMetadataProposalAction,
  mutateApplySceneMetadataPlan,
  mutateRejectSceneMetadataProposalAction,
  useSceneMetadataPlans,
} from "src/core/StashService";
import { useToast } from "src/hooks/Toast";
import { ModalComponent } from "src/components/Shared/Modal";

type Plan = GQL.SceneMetadataPlansQuery["sceneMetadataPlans"][number];
type Suggestion = Plan["suggested"][number];

interface ISceneMetadataPlanReviewModal {
  show: boolean;
  sceneIds: string[];
  onHide: () => void;
}

function stateVariant(state: GQL.SceneMetadataPlanState) {
  switch (state) {
    case GQL.SceneMetadataPlanState.Accepted:
      return "primary";
    case GQL.SceneMetadataPlanState.Applied:
      return "success";
    case GQL.SceneMetadataPlanState.Rejected:
    case GQL.SceneMetadataPlanState.Stale:
      return "secondary";
    default:
      return "warning";
  }
}

function stateLabel(state: GQL.SceneMetadataPlanState) {
  return state.toLowerCase().replace(/^./, (value) => value.toUpperCase());
}

function actionLabel(kind: string) {
  switch (kind) {
    case "performer_ids":
      return "Link performers";
    case "create_performer":
      return "Create performer";
    case "studio_id":
      return "Link studio";
    case "create_studio":
      return "Create studio";
    case "date":
      return "Set date";
    case "title":
      return "Set title";
    case "groups":
      return "Update groups";
    case "file_metadata":
      return "Update container metadata";
    case "remote_scene_match":
      return "Link remote scene";
    default:
      return kind.replaceAll("_", " ");
  }
}

function actionSummary(action: Suggestion) {
  let payload: Record<string, unknown>;
  try {
    payload = JSON.parse(action.payloadJSON) as Record<string, unknown>;
  } catch {
    return "The proposal payload could not be displayed.";
  }
  switch (action.kind) {
    case "title":
      return String(payload.title ?? "");
    case "date":
      return String(payload.date ?? "");
    case "studio_id":
      return `Studio #${String(payload.studioID ?? "")}`;
    case "performer_ids": {
      const ids = Array.isArray(payload.performerIDs)
        ? payload.performerIDs
        : [];
      return `${ids.length} performer${ids.length === 1 ? "" : "s"}`;
    }
    case "create_performer": {
      const performer = payload.performer as { name?: string } | undefined;
      return performer?.name ?? "New performer";
    }
    case "create_studio": {
      const studio = payload.studio as { name?: string } | undefined;
      return studio?.name ?? "New studio";
    }
    case "groups": {
      const groups = Array.isArray(payload.groups) ? payload.groups : [];
      return `${groups.length} group${groups.length === 1 ? "" : "s"}`;
    }
    case "remote_scene_match":
      return `${String(payload.endpoint ?? "")} · ${String(payload.remoteID ?? "")}`;
    case "file_metadata":
      return "Container title, comment, encoder, tags, and creation time";
    default:
      return "";
  }
}

export const SceneMetadataPlanReviewModal: React.FC<
  ISceneMetadataPlanReviewModal
> = ({ show, sceneIds, onHide }) => {
  const intl = useIntl();
  const Toast = useToast();
  const [busyKey, setBusyKey] = useState<string>();
  const { data, loading, error, refetch } = useSceneMetadataPlans(
    sceneIds,
    !show
  );
  const runs = useMemo(() => {
    const grouped = new Map<string, Plan[]>();
    for (const plan of data?.sceneMetadataPlans ?? []) {
      const plans = grouped.get(plan.runID) ?? [];
      plans.push(plan);
      grouped.set(plan.runID, plans);
    }
    return Array.from(grouped, ([runID, plans]) => ({ runID, plans }));
  }, [data]);

  async function reviewAction(
    runID: string,
    sceneID: string,
    actionID: string,
    accept: boolean
  ) {
    const key = `${accept ? "accept" : "reject"}:${actionID}`;
    setBusyKey(key);
    try {
      if (accept) {
        await mutateAcceptSceneMetadataProposalAction(runID, sceneID, [
          actionID,
        ]);
      } else {
        await mutateRejectSceneMetadataProposalAction(runID, sceneID, [
          actionID,
        ]);
      }
      await refetch();
    } catch (mutationError) {
      Toast.error(mutationError);
    } finally {
      setBusyKey(undefined);
    }
  }

  async function applyRun(runID: string, plans: Plan[]) {
    const acceptedSceneIDs = plans
      .filter((plan) =>
        plan.suggested.some(
          (action) => action.state === GQL.SceneMetadataPlanState.Accepted
        )
      )
      .map((plan) => plan.sceneID);
    if (acceptedSceneIDs.length === 0) return;
    const key = `apply:${runID}`;
    setBusyKey(key);
    try {
      await mutateApplySceneMetadataPlan(runID, acceptedSceneIDs);
      Toast.success(
        intl.formatMessage({
          id: "scene_metadata.review.applied",
          defaultMessage: "Accepted metadata changes applied",
        })
      );
      await refetch();
    } catch (mutationError) {
      Toast.error(mutationError);
    } finally {
      setBusyKey(undefined);
    }
  }

  return (
    <ModalComponent
      show={show}
      onHide={onHide}
      modalProps={{ size: "xl" }}
      icon={faClipboardCheck}
      header={intl.formatMessage({
        id: "scene_metadata.review.heading",
        defaultMessage: "Review metadata proposals",
      })}
      accept={{
        onClick: onHide,
        text: intl.formatMessage({
          id: "actions.close",
          defaultMessage: "Close",
        }),
      }}
    >
      {error ? <Alert variant="danger">{error.message}</Alert> : null}
      {loading && runs.length === 0 ? (
        <div className="text-center py-5" role="status">
          <Spinner animation="border" />
          <div className="mt-3">
            {intl.formatMessage({
              id: "scene_metadata.review.waiting",
              defaultMessage: "Waiting for analysis proposals…",
            })}
          </div>
        </div>
      ) : null}
      {!loading && runs.length === 0 ? (
        <Alert variant="info" className="mb-0">
          {intl.formatMessage({
            id: "scene_metadata.review.empty",
            defaultMessage:
              "No metadata proposals are available for the selected scenes. This view refreshes while analysis jobs run.",
          })}
        </Alert>
      ) : null}
      {runs.map(({ runID, plans }) => {
        const acceptedCount = plans.reduce(
          (count, plan) =>
            count +
            plan.suggested.filter(
              (action) => action.state === GQL.SceneMetadataPlanState.Accepted
            ).length,
          0
        );
        return (
          <section key={runID} className="border rounded mb-3">
            <header className="d-flex align-items-center justify-content-between bg-light border-bottom px-3 py-2">
              <div>
                <strong>
                  {intl.formatMessage(
                    {
                      id: "scene_metadata.review.run",
                      defaultMessage: "Analysis {run}",
                    },
                    { run: runID.slice(0, 8) }
                  )}
                </strong>
                <small className="text-muted ml-2">
                  {intl.formatDate(new Date(plans[0].createdAt), {
                    dateStyle: "medium",
                    timeStyle: "short",
                  })}
                </small>
              </div>
              <Button
                size="sm"
                variant="primary"
                disabled={acceptedCount === 0 || busyKey !== undefined}
                onClick={() => void applyRun(runID, plans)}
              >
                {busyKey === `apply:${runID}` ? (
                  <Spinner animation="border" size="sm" />
                ) : (
                  intl.formatMessage(
                    {
                      id: "scene_metadata.review.apply",
                      defaultMessage:
                        "Apply accepted changes ({count, number})",
                    },
                    { count: acceptedCount }
                  )
                )}
              </Button>
            </header>
            {plans.map((plan) => (
              <div key={plan.sceneID} className="px-3 py-3 border-bottom">
                <div className="d-flex align-items-center mb-2">
                  <Link to={`/scenes/${plan.sceneID}`}>
                    <strong>
                      {intl.formatMessage(
                        {
                          id: "scene_metadata.review.scene",
                          defaultMessage: "Scene #{id}",
                        },
                        { id: plan.sceneID }
                      )}
                    </strong>
                  </Link>
                  <Badge variant={stateVariant(plan.state)} className="ml-2">
                    {stateLabel(plan.state)}
                  </Badge>
                  {plan.ambiguity ? (
                    <Badge variant="warning" className="ml-2">
                      {intl.formatMessage({
                        id: "scene_metadata.review.ambiguous",
                        defaultMessage: "Needs attention",
                      })}
                    </Badge>
                  ) : null}
                </div>
                {plan.suggested.length === 0 ? (
                  <div className="text-muted small">
                    {intl.formatMessage({
                      id: "scene_metadata.review.no_changes",
                      defaultMessage: "No changes proposed.",
                    })}
                  </div>
                ) : null}
                {plan.suggested.map((action) => (
                  <div
                    key={action.actionID}
                    className="d-flex align-items-start justify-content-between border-top py-2"
                  >
                    <div className="pr-3">
                      <div>
                        <strong>{actionLabel(action.kind)}</strong>
                        <Badge
                          variant={stateVariant(action.state)}
                          className="ml-2"
                        >
                          {stateLabel(action.state)}
                        </Badge>
                      </div>
                      <div>{actionSummary(action)}</div>
                      {action.reasonCodes.length > 0 ? (
                        <small className="text-muted">
                          {action.reasonCodes
                            .map((reason) => reason.replaceAll("_", " "))
                            .join(" · ")}
                        </small>
                      ) : null}
                    </div>
                    {action.state === GQL.SceneMetadataPlanState.Proposed ? (
                      <div className="d-flex flex-shrink-0">
                        <Button
                          size="sm"
                          variant="outline-danger"
                          disabled={busyKey !== undefined}
                          onClick={() =>
                            void reviewAction(
                              runID,
                              plan.sceneID,
                              action.actionID,
                              false
                            )
                          }
                        >
                          {intl.formatMessage({
                            id: "actions.reject",
                            defaultMessage: "Reject",
                          })}
                        </Button>
                        <Button
                          size="sm"
                          variant="outline-success"
                          className="ml-2"
                          disabled={busyKey !== undefined}
                          onClick={() =>
                            void reviewAction(
                              runID,
                              plan.sceneID,
                              action.actionID,
                              true
                            )
                          }
                        >
                          {intl.formatMessage({
                            id: "actions.accept",
                            defaultMessage: "Accept",
                          })}
                        </Button>
                      </div>
                    ) : null}
                  </div>
                ))}
              </div>
            ))}
          </section>
        );
      })}
    </ModalComponent>
  );
};
