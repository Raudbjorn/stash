import React, { useEffect, useRef, useState } from "react";
import { Button, Form } from "react-bootstrap";
import { FormattedMessage, useIntl } from "react-intl";
import { BooleanSetting, Setting } from "../Inputs";
import { mutateConfigureAIServer } from "src/core/StashService";
import {
  AiServerConfigDataFragment,
  AiServerConfigInput,
  AiTaggingProvider,
} from "src/core/generated-graphql";
import { useToast } from "src/hooks/Toast";

interface AIServerProviderFormProps {
  enabled: boolean;
  config: AiServerConfigDataFragment;
  onSaved: () => void;
}

function toInput(
  enabled: boolean,
  config: AiServerConfigDataFragment
): AiServerConfigInput {
  return {
    enabled,
    taggingProvider: config.taggingProvider,
    taggingServerURL: config.taggingServerURL,
    taggingModelDir: config.taggingModelDir,
    taggingRulesDir: config.taggingRulesDir,
    taggingFrameInterval: config.taggingFrameInterval,
    taggingThreshold: config.taggingThreshold,
    taggingMaxSpanMerge: config.taggingMaxSpanMerge,
    taggingVLMModel: config.taggingVLMModel,
    taggingVLMLabels: config.taggingVLMLabels,
    taggingVLMGPULayers: config.taggingVLMGPULayers,
    taggingVLMContext: config.taggingVLMContext,
  };
}

export const AIServerProviderForm: React.FC<AIServerProviderFormProps> = ({
  enabled,
  config,
  onSaved,
}) => {
  const intl = useIntl();
  const Toast = useToast();
  const [form, setForm] = useState<AiServerConfigInput>(
    toInput(enabled, config)
  );
  const [openAIKey, setOpenAIKey] = useState("");
  const [saving, setSaving] = useState(false);
  // The status query polls every 5s with fetchPolicy "no-cache", so `config`
  // is a fresh object on every tick even when nothing changed. Re-seeding the
  // form unconditionally on that effect would wipe out an in-progress edit
  // before the user finishes it. Skip re-seeding while there is an unsaved
  // edit, and only pick up fresh values again once a save has gone through.
  const dirty = useRef(false);

  useEffect(() => {
    if (dirty.current) return;
    setForm(toInput(enabled, config));
  }, [enabled, config]);

  function update(patch: Partial<AiServerConfigInput>) {
    dirty.current = true;
    setForm((prev) => ({ ...prev, ...patch }));
  }

  async function save() {
    setSaving(true);
    try {
      await mutateConfigureAIServer({
        ...form,
        taggingOpenAIKey: openAIKey ? openAIKey : undefined,
      });
      setOpenAIKey("");
      dirty.current = false;
      Toast.success(intl.formatMessage({ id: "config.ai_server.saved" }));
      onSaved();
    } catch (error) {
      Toast.error(error);
    } finally {
      setSaving(false);
    }
  }

  const provider = form.taggingProvider ?? undefined;

  return (
    <>
      <BooleanSetting
        id="ai-server-enabled"
        headingID="config.ai_server.enable"
        subHeadingID="config.ai_server.enable_description"
        checked={form.enabled}
        onChange={(v) => update({ enabled: v })}
      />

      <Setting
        headingID="config.ai_server.provider"
        subHeadingID="config.ai_server.provider_description"
      >
        <Form.Control
          as="select"
          className="input-control"
          value={provider ?? ""}
          onChange={(e) =>
            update({
              taggingProvider:
                (e.currentTarget.value as AiTaggingProvider) || undefined,
            })
          }
        >
          <option value="">
            {intl.formatMessage({ id: "config.ai_server.provider_none" })}
          </option>
          <option value={AiTaggingProvider.Native}>
            {intl.formatMessage({ id: "config.ai_server.provider_native" })}
          </option>
          <option value={AiTaggingProvider.LlamaVlm}>
            {intl.formatMessage({
              id: "config.ai_server.provider_llama_vlm",
            })}
          </option>
          <option value={AiTaggingProvider.SkierAitagging}>
            {intl.formatMessage({ id: "config.ai_server.provider_remote" })}
          </option>
          <option value={AiTaggingProvider.OpenaiModeration}>
            {intl.formatMessage({
              id: "config.ai_server.provider_moderation",
            })}
          </option>
        </Form.Control>
      </Setting>

      {provider === AiTaggingProvider.SkierAitagging && (
        <Setting
          headingID="config.ai_server.server_url"
          subHeadingID="config.ai_server.server_url_description"
        >
          <Form.Control
            className="text-input"
            value={form.taggingServerURL}
            onChange={(e) =>
              update({ taggingServerURL: e.currentTarget.value })
            }
          />
        </Setting>
      )}

      {provider === AiTaggingProvider.OpenaiModeration && (
        <Setting
          headingID="config.ai_server.openai_key"
          subHeadingID="config.ai_server.openai_key_description"
        >
          <Form.Control
            type="password"
            className="text-input"
            placeholder={
              config.taggingOpenAIKeySet
                ? intl.formatMessage({ id: "config.ai_server.openai_key_set" })
                : ""
            }
            value={openAIKey}
            onChange={(e) => setOpenAIKey(e.currentTarget.value)}
          />
        </Setting>
      )}

      {provider === AiTaggingProvider.LlamaVlm && (
        <>
          <Setting
            headingID="config.ai_server.vlm_model"
            subHeadingID="config.ai_server.vlm_model_description"
          >
            <Form.Control
              className="text-input"
              value={form.taggingVLMModel}
              onChange={(e) =>
                update({ taggingVLMModel: e.currentTarget.value })
              }
            />
          </Setting>
          <Setting
            headingID="config.ai_server.vlm_gpu_layers"
            subHeadingID="config.ai_server.vlm_gpu_layers_description"
          >
            <Form.Control
              type="number"
              min={0}
              className="text-input"
              value={form.taggingVLMGPULayers}
              onChange={(e) =>
                update({
                  taggingVLMGPULayers: Number(e.currentTarget.value) || 0,
                })
              }
            />
          </Setting>
          <Setting
            headingID="config.ai_server.vlm_context"
            subHeadingID="config.ai_server.vlm_context_description"
          >
            <Form.Control
              type="number"
              min={0}
              className="text-input"
              value={form.taggingVLMContext}
              onChange={(e) =>
                update({
                  taggingVLMContext: Number(e.currentTarget.value) || 0,
                })
              }
            />
          </Setting>
        </>
      )}

      <div className="ai-server-save">
        <Button disabled={saving} onClick={save}>
          <FormattedMessage id="actions.save" />
        </Button>
      </div>
    </>
  );
};
