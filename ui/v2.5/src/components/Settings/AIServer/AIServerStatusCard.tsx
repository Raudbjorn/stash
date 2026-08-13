import React from "react";
import { Badge } from "react-bootstrap";
import { FormattedMessage } from "react-intl";
import { Setting } from "../Inputs";
import {
  AiServerState,
  AiServerStatusDataFragment,
} from "src/core/generated-graphql";

interface AIServerStatusCardProps {
  status: AiServerStatusDataFragment;
}

function stateVariant(state: AiServerState) {
  switch (state) {
    case AiServerState.Ready:
      return "success";
    case AiServerState.Failed:
      return "danger";
    case AiServerState.Stopped:
      return "warning";
    default:
      return "secondary";
  }
}

export const AIServerStatusCard: React.FC<AIServerStatusCardProps> = ({
  status,
}) => {
  return (
    <Setting
      headingID="config.ai_server.status_heading"
      subHeadingID="config.ai_server.status_description"
    >
      <div>
        <Badge variant={stateVariant(status.state)}>
          <FormattedMessage
            id={`config.ai_server.state.${status.state.toLowerCase()}`}
          />
        </Badge>
        {status.backendVersion && (
          <span className="ai-server-version"> v{status.backendVersion}</span>
        )}
        <div className="ai-server-health">
          <Badge
            variant={status.database.status === "ok" ? "success" : "warning"}
          >
            <FormattedMessage id="config.ai_server.database" />
          </Badge>{" "}
          {status.database.message}
        </div>
        {status.error && <div className="text-danger">{status.error}</div>}
        {status.enabled && !status.hasVLMProvider && (
          <div className="text-muted">
            <FormattedMessage id="config.ai_server.no_vlm_provider" />
          </div>
        )}
      </div>
    </Setting>
  );
};
