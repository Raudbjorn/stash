import React, { useEffect, useState } from "react";
import { Badge, Button, Form } from "react-bootstrap";
import { FormattedMessage, useIntl } from "react-intl";
import { BooleanSetting, Setting } from "../Inputs";
import {
  mutateConfigureOllama,
  useOllamaSettings,
} from "src/core/StashService";
import { OllamaBackend, OllamaConfigInput } from "src/core/generated-graphql";
import { useToast } from "src/hooks/Toast";
import { LoadingIndicator } from "src/components/Shared/LoadingIndicator";

export const OllamaSettingsCard: React.FC = () => {
  const intl = useIntl();
  const Toast = useToast();
  const { data, loading, refetch } = useOllamaSettings();
  // mistralApiKey is intentionally absent from this state: the query never
  // returns the effective key (which may fall back to $MISTRAL_API_KEY), only
  // whether one is set, so there is nothing to seed it from. It is entered
  // fresh in mistralApiKeyInput below and only sent when non-empty.
  const [form, setForm] = useState<
    Omit<OllamaConfigInput, "mistralApiKey"> | undefined
  >();
  const [mistralApiKeyInput, setMistralApiKeyInput] = useState("");
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    if (!data?.ollamaStatus) return;
    const { config } = data.ollamaStatus;
    setForm({
      baseUrl: config.baseUrl,
      backend: config.backend,
      model: config.model,
      timeout: config.timeout,
      enabled: config.enabled,
      fallbackToTraditionalDict: config.fallbackToTraditionalDict,
      promptTemplate: config.promptTemplate,
    });
    setMistralApiKeyInput("");
  }, [data]);

  if (loading && !data) return <LoadingIndicator />;
  if (!data?.ollamaStatus || !form) return null;

  const { available, config } = data.ollamaStatus;

  function update(patch: Partial<Omit<OllamaConfigInput, "mistralApiKey">>) {
    setForm((prev) => (prev ? { ...prev, ...patch } : prev));
  }

  async function save() {
    if (!form) return;
    setSaving(true);
    try {
      await mutateConfigureOllama({
        ...form,
        mistralApiKey: mistralApiKeyInput ? mistralApiKeyInput : undefined,
      });
      setMistralApiKeyInput("");
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
            id={
              available
                ? "config.ollama.available"
                : "config.ollama.unavailable"
            }
          />
        </Badge>
      </Setting>

      <BooleanSetting
        id="ollama-enabled"
        headingID="config.ollama.enable"
        checked={form.enabled}
        onChange={(v) => update({ enabled: v })}
      />

      <Setting
        headingID="config.ollama.backend"
        subHeadingID="config.ollama.backend_description"
      >
        <Form.Control
          as="select"
          className="input-control"
          value={form.backend}
          onChange={(e) =>
            update({ backend: e.currentTarget.value as OllamaBackend })
          }
        >
          <option value={OllamaBackend.Ollama}>
            {intl.formatMessage({ id: "config.ollama.backend_ollama" })}
          </option>
          <option value={OllamaBackend.OpenaiCompatible}>
            {intl.formatMessage({
              id: "config.ollama.backend_openai_compatible",
            })}
          </option>
        </Form.Control>
      </Setting>

      <Setting
        headingID="config.ollama.base_url"
        subHeadingID="config.ollama.base_url_description"
      >
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

      <Setting
        headingID="config.ollama.mistral_key"
        subHeadingID="config.ollama.mistral_key_description"
      >
        <Form.Control
          type="password"
          className="text-input"
          placeholder={
            config.mistralApiKeySet
              ? intl.formatMessage({ id: "config.ai_server.openai_key_set" })
              : ""
          }
          value={mistralApiKeyInput}
          onChange={(e) => setMistralApiKeyInput(e.currentTarget.value)}
        />
      </Setting>

      <div className="ai-server-save">
        <Button disabled={saving} onClick={save}>
          <FormattedMessage id="actions.save" />
        </Button>
      </div>
    </>
  );
};
