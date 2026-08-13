import React, { useEffect, useState } from "react";
import { Badge, Button, Form } from "react-bootstrap";
import { FormattedMessage, useIntl } from "react-intl";
import { BooleanSetting, Setting } from "../Inputs";
import { mutateConfigureOllama, useOllamaSettings } from "src/core/StashService";
import { OllamaConfigInput } from "src/core/generated-graphql";
import { useToast } from "src/hooks/Toast";
import { LoadingIndicator } from "src/components/Shared/LoadingIndicator";

export const OllamaSettingsCard: React.FC = () => {
  const intl = useIntl();
  const Toast = useToast();
  const { data, loading, refetch } = useOllamaSettings();
  const [form, setForm] = useState<OllamaConfigInput | undefined>();
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    if (!data?.ollamaStatus) return;
    const { config } = data.ollamaStatus;
    setForm({
      baseUrl: config.baseUrl,
      model: config.model,
      timeout: config.timeout,
      enabled: config.enabled,
      fallbackToTraditionalDict: config.fallbackToTraditionalDict,
      promptTemplate: config.promptTemplate,
      mistralApiKey: config.mistralApiKey,
    });
  }, [data]);

  if (loading && !data) return <LoadingIndicator />;
  if (!data?.ollamaStatus || !form) return null;

  const { available } = data.ollamaStatus;

  function update(patch: Partial<OllamaConfigInput>) {
    setForm((prev) => (prev ? { ...prev, ...patch } : prev));
  }

  async function save() {
    if (!form) return;
    setSaving(true);
    try {
      await mutateConfigureOllama(form);
      Toast.success(intl.formatMessage({ id: "config.ai_server.saved" }));
      await refetch();
    } catch (error) {
      Toast.error(error);
    } finally {
      setSaving(false);
    }
  }

  return (
    <>
      <Setting
        headingID="config.ollama.status_heading"
        subHeadingID="config.ollama.status_description"
      >
        <Badge variant={available ? "success" : "secondary"}>
          <FormattedMessage
            id={available ? "config.ollama.available" : "config.ollama.unavailable"}
          />
        </Badge>
      </Setting>

      <BooleanSetting
        id="ollama-enabled"
        headingID="config.ollama.enable"
        checked={form.enabled}
        onChange={(v) => update({ enabled: v })}
      />

      <Setting headingID="config.ollama.base_url">
        <Form.Control
          className="text-input"
          value={form.baseUrl}
          onChange={(e) => update({ baseUrl: e.currentTarget.value })}
        />
      </Setting>

      <Setting headingID="config.ollama.model">
        <Form.Control
          className="text-input"
          value={form.model}
          onChange={(e) => update({ model: e.currentTarget.value })}
        />
      </Setting>

      <BooleanSetting
        id="ollama-fallback"
        headingID="config.ollama.fallback"
        checked={form.fallbackToTraditionalDict}
        onChange={(v) => update({ fallbackToTraditionalDict: v })}
      />

      <div className="ai-server-save">
        <Button disabled={saving} onClick={save}>
          <FormattedMessage id="actions.save" />
        </Button>
      </div>
    </>
  );
};
