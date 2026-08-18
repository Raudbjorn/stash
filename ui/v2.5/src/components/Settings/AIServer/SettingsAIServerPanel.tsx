import React from "react";
import { LoadingIndicator } from "src/components/Shared/LoadingIndicator";
import { useAIServerSettings } from "src/core/StashService";
import { SettingSection } from "../SettingSection";
import { AIServerStatusCard } from "./AIServerStatusCard";
import { AIServerProviderForm } from "./AIServerProviderForm";
import { AITaskTable } from "./AITaskTable";
import { OllamaSettingsCard } from "./OllamaSettingsCard";
import "./styles.scss";

export const SettingsAIServerPanel: React.FC = () => {
  const { data, loading, error, refetch } = useAIServerSettings();

  if (loading && !data) return <LoadingIndicator />;
  if (error && !data) {
    return <div className="text-danger">{error.message}</div>;
  }
  if (!data) return null;

  const { aiServerStatus, aiTasks, aiTaskHistory } = data;

  return (
    <div id="ai-server-settings">
      <SettingSection
        headingID="config.categories.ai_server"
        subHeadingID="config.ai_server.description"
      >
        <AIServerStatusCard
          status={aiServerStatus}
          hasLocalTextProvider={aiServerStatus.hasLocalTextProvider}
        />
        <AIServerProviderForm
          enabled={aiServerStatus.enabled}
          config={aiServerStatus.config}
          availableVLMModels={aiServerStatus.availableVLMModels}
          availableVoyageRerankModels={
            aiServerStatus.availableVoyageRerankModels
          }
          availableVoyageVideoModels={aiServerStatus.availableVoyageVideoModels}
          onSaved={() => void refetch()}
        />
      </SettingSection>

      <SettingSection
        headingID="config.ai_server.tasks_heading"
        subHeadingID="config.ai_server.tasks_description"
      >
        <AITaskTable tasks={aiTasks} history={aiTaskHistory} />
      </SettingSection>

      <SettingSection
        headingID="config.ollama.heading"
        subHeadingID="config.ollama.description"
      >
        <OllamaSettingsCard />
      </SettingSection>
    </div>
  );
};
