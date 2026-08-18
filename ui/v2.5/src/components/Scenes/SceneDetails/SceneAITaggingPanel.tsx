import React, { useEffect, useMemo, useState } from "react";
import { Button, Form } from "react-bootstrap";
import { FormattedMessage, useIntl } from "react-intl";
import {
  mutateAnalyzeSceneWithAI,
  refreshSceneAfterAI,
  useAIServerAvailability,
  useSceneAITaggingSpans,
} from "src/core/StashService";
import { AiTaskStatus } from "src/core/generated-graphql";
import { useToast } from "src/hooks/Toast";
import { LoadingIndicator } from "src/components/Shared/LoadingIndicator";
import TextUtils from "src/utils/text";

interface ISceneAITaggingPanelProps {
  sceneId: string;
  isVisible: boolean;
}

interface AnalysisService {
  id: string;
  label: string;
}

const ANALYSIS_POLL_TIMEOUT_MS = 60 * 60 * 1000;
const ANALYSIS_POLL_INTERVAL_MS = 4000;

export const SceneAITaggingPanel: React.FC<ISceneAITaggingPanelProps> = ({
  sceneId,
  isVisible,
}) => {
  const intl = useIntl();
  const Toast = useToast();
  const [analyzing, setAnalyzing] = useState(false);
  const [awaitingResult, setAwaitingResult] = useState(false);
  const [submittedTaskID, setSubmittedTaskID] = useState<string>();
  const [selectedService, setSelectedService] = useState("");

  const { data: availability } = useAIServerAvailability();
  const taggingStatus = availability?.aiTaggingStatus;
  const aiAvailable = !!taggingStatus?.available;
  const localAvailable = !!taggingStatus?.localAvailable;
  const voyageAvailable = !!taggingStatus?.voyageAvailable;
  const provider = taggingStatus?.provider ?? "";
  const config = availability?.aiServerStatus.config;
  const services = useMemo<AnalysisService[]>(() => {
    if (!aiAvailable) return [];
    const configured: AnalysisService[] = [];
    if (localAvailable && provider === "llama_vlm") {
      configured.push({
        id: "local",
        label: intl.formatMessage({ id: "ai_tagging.service_local" }),
      });
      if (
        config?.taggingVLMVoyageAPIKeySet &&
        config.taggingVLMVoyageRerankModel !== ""
      ) {
        configured.push({
          id: "local_voyage",
          label: intl.formatMessage({ id: "ai_tagging.service_voyage" }),
        });
      }
    } else if (localAvailable && provider !== "") {
      configured.push({
        id: provider,
        label: provider.replaceAll("_", " "),
      });
    }
    if (voyageAvailable) {
      configured.push({
        id: "voyage",
        label: intl.formatMessage({ id: "ai_tagging.service_voyage_only" }),
      });
    }
    return configured;
  }, [aiAvailable, config, intl, localAvailable, provider, voyageAvailable]);

  useEffect(() => {
    if (!services.some((service) => service.id === selectedService)) {
      setSelectedService(services[0]?.id ?? "");
    }
  }, [selectedService, services]);

  const { data, loading, refetch } = useSceneAITaggingSpans(
    sceneId,
    selectedService,
    isVisible ? ANALYSIS_POLL_INTERVAL_MS : 0
  );
  const activeTasks = data?.aiTasks ?? [];
  const serviceRunning = activeTasks.some(
    (task) =>
      task.status === AiTaskStatus.Running ||
      task.status === AiTaskStatus.Streaming
  );
  const sceneHistory = (data?.aiTaskHistory ?? []).filter(
    (task) => task.actionID === "ai.tagging.analyze" && task.itemID === sceneId
  );

  useEffect(() => {
    if (!submittedTaskID) return;
    const completed = sceneHistory.some((task) => task.id === submittedTaskID);
    if (completed) {
      setAwaitingResult(false);
      setSubmittedTaskID(undefined);
      refreshSceneAfterAI(sceneId);
    }
  }, [sceneHistory, sceneId, submittedTaskID]);
  useEffect(() => {
    if (!awaitingResult) return;
    const timeout = setTimeout(() => {
      setAwaitingResult(false);
      setSubmittedTaskID(undefined);
    }, ANALYSIS_POLL_TIMEOUT_MS);
    return () => clearTimeout(timeout);
  }, [awaitingResult]);

  if (!isVisible) return null;

  async function onAnalyze() {
    setAnalyzing(true);
    try {
      const response = await mutateAnalyzeSceneWithAI(sceneId, selectedService);
      const submission = response.data?.analyzeSceneWithAI;
      if (submission?.alreadyInProgress) {
        Toast.success(
          intl.formatMessage({ id: "ai_tagging.already_in_progress" })
        );
      } else {
        Toast.success(intl.formatMessage({ id: "ai_tagging.queued" }));
      }
      if (submission) {
        setSubmittedTaskID(submission.taskID);
        setAwaitingResult(true);
      }
    } catch (error) {
      Toast.error(error);
    } finally {
      setAnalyzing(false);
      void refetch();
    }
  }

  const groups = data?.aiTaggingSpans ?? [];

  return (
    <div className="scene-ai-tagging-panel">
      <div className="scene-ai-tagging-controls d-flex flex-wrap align-items-end gap-2 mb-3">
        <Form.Group>
          <Form.Label>
            <FormattedMessage id="ai_tagging.service" />
          </Form.Label>
          <Form.Control
            as="select"
            className="input-control"
            aria-label={intl.formatMessage({ id: "ai_tagging.service" })}
            value={selectedService}
            disabled={!aiAvailable || services.length === 0}
            onChange={(event) => setSelectedService(event.currentTarget.value)}
          >
            {services.map((service) => (
              <option key={service.id} value={service.id}>
                {service.label}
              </option>
            ))}
          </Form.Control>
        </Form.Group>
        <Button
          disabled={!aiAvailable || selectedService === "" || analyzing}
          onClick={onAnalyze}
        >
          {analyzing ? (
            <FormattedMessage id="ai_tagging.queuing" />
          ) : serviceRunning ? (
            <FormattedMessage id="ai_tagging.queue_analysis" />
          ) : (
            <FormattedMessage id="ai_tagging.analyze" />
          )}
        </Button>
        {awaitingResult && (
          <span className="text-muted">
            <FormattedMessage id="ai_tagging.awaiting_result" />
          </span>
        )}
        {!aiAvailable && (
          <div className="text-muted">
            <FormattedMessage id="ai_tagging.unavailable_help" />
          </div>
        )}
      </div>

      {loading && !data ? (
        <LoadingIndicator />
      ) : groups.length === 0 ? (
        <div className="scene-ai-tagging-empty text-muted">
          <FormattedMessage id="ai_tagging.empty" />
        </div>
      ) : (
        <table className="scene-ai-tagging-table table">
          <thead>
            <tr>
              <th>
                <FormattedMessage id="ai_tagging.label" />
              </th>
              <th>
                <FormattedMessage id="ai_tagging.time" />
              </th>
              <th>
                <FormattedMessage id="ai_tagging.confidence" />
              </th>
            </tr>
          </thead>
          <tbody>
            {groups.map((group) =>
              group.spans.map((span, index) => (
                <tr key={`${group.category}-${group.label}-${index}`}>
                  <td>
                    {group.category}: {group.label}
                  </td>
                  <td>
                    {TextUtils.secondsToTimestamp(span.start)} -{" "}
                    {TextUtils.secondsToTimestamp(span.end)}
                  </td>
                  <td>
                    {span.confidence != null
                      ? `${Math.round(span.confidence * 100)}%`
                      : ""}
                  </td>
                </tr>
              ))
            )}
          </tbody>
        </table>
      )}

      {sceneHistory.length > 0 && (
        <details className="mt-3">
          <summary>
            <FormattedMessage id="ai_tagging.history" />
          </summary>
          {sceneHistory.slice(0, 5).map((task) => (
            <details className="mt-2" key={task.id}>
              <summary>
                {String(
                  (task.parameters as Record<string, unknown> | null)
                    ?.analysis_service ?? task.service
                )}{" "}
                — {task.status}
              </summary>
              {task.error && <div className="text-danger">{task.error}</div>}
              {task.result != null && (
                <pre className="mt-2">
                  {JSON.stringify(task.result, null, 2)}
                </pre>
              )}
            </details>
          ))}
        </details>
      )}
    </div>
  );
};

export default SceneAITaggingPanel;
