import React, { useEffect, useRef, useState } from "react";
import { Button } from "react-bootstrap";
import { FormattedMessage, useIntl } from "react-intl";
import {
  mutateAnalyzeSceneWithAI,
  useAIServerAvailability,
  useSceneAITaggingSpans,
} from "src/core/StashService";
import { useToast } from "src/hooks/Toast";
import { LoadingIndicator } from "src/components/Shared/LoadingIndicator";
import TextUtils from "src/utils/text";

interface ISceneAITaggingPanelProps {
  sceneId: string;
  isVisible: boolean;
}

// How long to keep polling for a result after triggering analysis, since
// analyzeSceneWithAI only queues the work and returns immediately. Bounded so
// a stuck/failed task doesn't poll forever.
const ANALYSIS_POLL_TIMEOUT_MS = 3 * 60 * 1000;
const ANALYSIS_POLL_INTERVAL_MS = 4000;

export const SceneAITaggingPanel: React.FC<ISceneAITaggingPanelProps> = ({
  sceneId,
  isVisible,
}) => {
  const intl = useIntl();
  const Toast = useToast();
  const [analyzing, setAnalyzing] = useState(false);
  const [awaitingResult, setAwaitingResult] = useState(false);

  const { data: availability } = useAIServerAvailability();
  // aiTaggingStatus reflects whichever provider is actually configured
  // (native/llama_vlm/remote/moderation); hasVLMProvider only tracks the
  // local VLM provider used by the scene-metadata analyzer's "local AI"
  // options and would wrongly disable this button for the other providers.
  const aiAvailable = !!availability?.aiTaggingStatus.available;

  const { data, loading, refetch } = useSceneAITaggingSpans(
    sceneId,
    awaitingResult ? ANALYSIS_POLL_INTERVAL_MS : 0
  );

  const groupCount = data?.aiTaggingSpans.length ?? 0;
  const previousGroupCount = useRef(groupCount);
  useEffect(() => {
    if (awaitingResult && groupCount > previousGroupCount.current) {
      setAwaitingResult(false);
    }
    previousGroupCount.current = groupCount;
  }, [groupCount, awaitingResult]);

  useEffect(() => {
    if (!awaitingResult) return;
    const timeout = setTimeout(
      () => setAwaitingResult(false),
      ANALYSIS_POLL_TIMEOUT_MS
    );
    return () => clearTimeout(timeout);
  }, [awaitingResult]);

  if (!isVisible) return null;

  async function onAnalyze() {
    setAnalyzing(true);
    try {
      const result = await mutateAnalyzeSceneWithAI(sceneId);
      if (result.data?.analyzeSceneWithAI.alreadyInProgress) {
        Toast.success(
          intl.formatMessage({ id: "ai_tagging.already_in_progress" })
        );
      } else {
        Toast.success(intl.formatMessage({ id: "ai_tagging.queued" }));
      }
      setAwaitingResult(true);
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
      <div className="scene-ai-tagging-controls">
        <Button disabled={!aiAvailable || analyzing} onClick={onAnalyze}>
          <FormattedMessage id="ai_tagging.analyze" />
        </Button>
        {!aiAvailable && (
          <div className="text-muted">
            <FormattedMessage id="ai_tagging.unavailable_help" />
          </div>
        )}
      </div>

      {loading ? (
        <LoadingIndicator />
      ) : groups.length === 0 ? (
        <div className="scene-ai-tagging-empty text-muted">
          <FormattedMessage id="ai_tagging.empty" />
        </div>
      ) : (
        <table className="scene-ai-tagging-table">
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
              group.spans.map((span, i) => (
                <tr key={`${group.category}-${group.label}-${i}`}>
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
    </div>
  );
};

export default SceneAITaggingPanel;
