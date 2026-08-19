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
  availableVLMModels: string[];
  availableVoyageRerankModels: string[];
  availableVoyageVideoModels: string[];
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
    taggingAnalyzeMode: config.taggingAnalyzeMode,
    taggingVLMAcceptMode: config.taggingVLMAcceptMode,
    taggingVLMVoyageRerankModel: config.taggingVLMVoyageRerankModel,
    taggingVLMVoyageRerankTopK: config.taggingVLMVoyageRerankTopK,
    taggingVLMVoyageEndpoint: config.taggingVLMVoyageEndpoint,
    taggingVLMVoyageVideoEnabled: config.taggingVLMVoyageVideoEnabled,
    taggingVLMVoyageVideoModel: config.taggingVLMVoyageVideoModel,
    taggingVLMVoyageSegmentSecs: config.taggingVLMVoyageSegmentSecs,
    taggingVLMVoyageDimension: config.taggingVLMVoyageDimension,
    taggingVLMVoyageEmbeddingEndpoint: config.taggingVLMVoyageEmbeddingEndpoint,
    taggingTaxonomyEndpoint: config.taggingTaxonomyEndpoint,
    taggingTaxonomyCategories: config.taggingTaxonomyCategories,
    taggingTaxonomyMaxCandidates: config.taggingTaxonomyMaxCandidates,
  };
}

export const AIServerProviderForm: React.FC<AIServerProviderFormProps> = ({
  enabled,
  config,
  availableVLMModels,
  availableVoyageRerankModels,
  availableVoyageVideoModels,
  onSaved,
}) => {
  const intl = useIntl();
  const Toast = useToast();
  const [form, setForm] = useState<AiServerConfigInput>(
    toInput(enabled, config)
  );
  const [openAIKey, setOpenAIKey] = useState("");
  const [voyageAPIKey, setVoyageAPIKey] = useState("");
  const [taxonomyAPIKey, setTaxonomyAPIKey] = useState("");
  const [taxonomyCategories, setTaxonomyCategories] = useState(
    config.taggingTaxonomyCategories.join(", ")
  );
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
    setTaxonomyCategories(config.taggingTaxonomyCategories.join(", "));
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
        taggingOpenAIKey: openAIKey || undefined,
        taggingVLMVoyageAPIKey: voyageAPIKey || undefined,
        taggingTaxonomyAPIKey: taxonomyAPIKey || undefined,
        taggingTaxonomyCategories: taxonomyCategories
          .split(",")
          .map((category) => category.trim())
          .filter(Boolean),
      });
      setOpenAIKey("");
      setVoyageAPIKey("");
      setTaxonomyAPIKey("");
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
  const taxonomySettingsVisible =
    (provider === AiTaggingProvider.LlamaVlm &&
      form.taggingAnalyzeMode === "taxonomy") ||
    form.taggingVLMVoyageVideoEnabled;

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
              as="select"
              className="input-control"
              value={form.taggingVLMModel}
              onChange={(e) =>
                update({ taggingVLMModel: e.currentTarget.value })
              }
            >
              {!availableVLMModels.includes(form.taggingVLMModel) && (
                <option value={form.taggingVLMModel} disabled>
                  {intl.formatMessage(
                    { id: "config.ai_server.vlm_model_unavailable" },
                    { model: form.taggingVLMModel }
                  )}
                </option>
              )}
              {availableVLMModels.map((model) => (
                <option key={model} value={model}>
                  {model}
                </option>
              ))}
            </Form.Control>
          </Setting>
          <Setting
            headingID="config.ai_server.vlm_analyze_mode"
            subHeadingID="config.ai_server.vlm_analyze_mode_description"
          >
            <Form.Control
              as="select"
              className="input-control"
              value={form.taggingAnalyzeMode}
              onChange={(e) =>
                update({ taggingAnalyzeMode: e.currentTarget.value })
              }
            >
              <option value="legacy">
                {intl.formatMessage({
                  id: "config.ai_server.vlm_analyze_mode_legacy",
                })}
              </option>
              <option value="taxonomy">
                {intl.formatMessage({
                  id: "config.ai_server.vlm_analyze_mode_taxonomy",
                })}
              </option>
            </Form.Control>
          </Setting>
          <Setting
            headingID="config.ai_server.vlm_accept_mode"
            subHeadingID="config.ai_server.vlm_accept_mode_description"
          >
            <Form.Control
              as="select"
              className="input-control"
              value={form.taggingVLMAcceptMode}
              onChange={(e) =>
                update({ taggingVLMAcceptMode: e.currentTarget.value })
              }
            >
              <option value="shadow">
                {intl.formatMessage({
                  id: "config.ai_server.vlm_accept_mode_shadow",
                })}
              </option>
              <option value="rescue">
                {intl.formatMessage({
                  id: "config.ai_server.vlm_accept_mode_rescue",
                })}
              </option>
              <option value="strict">
                {intl.formatMessage({
                  id: "config.ai_server.vlm_accept_mode_strict",
                })}
              </option>
            </Form.Control>
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
      {taxonomySettingsVisible && (
        <>
          <Setting
            headingID="config.ai_server.taxonomy_endpoint"
            subHeadingID="config.ai_server.taxonomy_endpoint_description"
          >
            <Form.Control
              className="text-input"
              type="url"
              required
              value={form.taggingTaxonomyEndpoint}
              onChange={(event) =>
                update({ taggingTaxonomyEndpoint: event.currentTarget.value })
              }
            />
          </Setting>
          <Setting
            headingID="config.ai_server.taxonomy_api_key"
            subHeadingID="config.ai_server.taxonomy_api_key_description"
          >
            <Form.Control
              className="text-input"
              type="password"
              placeholder={
                config.taggingTaxonomyAPIKeySet
                  ? intl.formatMessage({
                      id: "config.ai_server.taxonomy_api_key_set",
                    })
                  : ""
              }
              value={taxonomyAPIKey}
              onChange={(event) => setTaxonomyAPIKey(event.currentTarget.value)}
            />
          </Setting>
          <Setting
            headingID="config.ai_server.taxonomy_categories"
            subHeadingID="config.ai_server.taxonomy_categories_description"
          >
            <Form.Control
              className="text-input"
              value={taxonomyCategories}
              onChange={(event) => {
                dirty.current = true;
                setTaxonomyCategories(event.currentTarget.value);
              }}
            />
          </Setting>
          <Setting
            headingID="config.ai_server.taxonomy_max_candidates"
            subHeadingID="config.ai_server.taxonomy_max_candidates_description"
          >
            <Form.Control
              className="text-input"
              type="number"
              min={0}
              value={form.taggingTaxonomyMaxCandidates}
              onChange={(event) =>
                update({
                  taggingTaxonomyMaxCandidates:
                    Number.parseInt(event.currentTarget.value, 10) || 0,
                })
              }
            />
          </Setting>
        </>
      )}
      <Setting
        headingID="config.ai_server.voyage_api_key"
        subHeadingID="config.ai_server.voyage_api_key_description"
      >
        <Form.Control
          type="password"
          className="text-input"
          placeholder={
            config.taggingVLMVoyageAPIKeySet
              ? intl.formatMessage({
                  id: "config.ai_server.voyage_api_key_set",
                })
              : ""
          }
          value={voyageAPIKey}
          onChange={(e) => setVoyageAPIKey(e.currentTarget.value)}
        />
      </Setting>
      <Setting
        headingID="config.ai_server.voyage_rerank_model"
        subHeadingID="config.ai_server.voyage_rerank_model_description"
      >
        <Form.Control
          as="select"
          className="input-control"
          value={form.taggingVLMVoyageRerankModel}
          onChange={(e) =>
            update({
              taggingVLMVoyageRerankModel: e.currentTarget.value,
            })
          }
        >
          <option value="">
            {intl.formatMessage({
              id: "config.ai_server.voyage_rerank_model_disabled",
            })}
          </option>
          {!availableVoyageRerankModels.includes(
            form.taggingVLMVoyageRerankModel
          ) &&
            form.taggingVLMVoyageRerankModel !== "" && (
              <option value={form.taggingVLMVoyageRerankModel} disabled>
                {intl.formatMessage(
                  { id: "config.ai_server.vlm_model_unavailable" },
                  { model: form.taggingVLMVoyageRerankModel }
                )}
              </option>
            )}
          {availableVoyageRerankModels.map((model) => (
            <option key={model} value={model}>
              {model}
            </option>
          ))}
        </Form.Control>
      </Setting>
      <BooleanSetting
        id="ai-server-voyage-video-enabled"
        headingID="config.ai_server.voyage_video_enabled"
        subHeadingID="config.ai_server.voyage_video_enabled_description"
        checked={form.taggingVLMVoyageVideoEnabled}
        onChange={(v) => update({ taggingVLMVoyageVideoEnabled: v })}
      />
      <Setting
        headingID="config.ai_server.voyage_video_model"
        subHeadingID="config.ai_server.voyage_video_model_description"
      >
        <Form.Control
          as="select"
          className="input-control"
          value={form.taggingVLMVoyageVideoModel}
          onChange={(e) =>
            update({ taggingVLMVoyageVideoModel: e.currentTarget.value })
          }
        >
          {!availableVoyageVideoModels.includes(
            form.taggingVLMVoyageVideoModel
          ) && (
            <option value={form.taggingVLMVoyageVideoModel} disabled>
              {intl.formatMessage(
                { id: "config.ai_server.vlm_model_unavailable" },
                { model: form.taggingVLMVoyageVideoModel }
              )}
            </option>
          )}
          {availableVoyageVideoModels.map((model) => (
            <option key={model} value={model}>
              {model}
            </option>
          ))}
        </Form.Control>
      </Setting>

      <div className="ai-server-save">
        <Button disabled={saving} onClick={save}>
          <FormattedMessage id="actions.save" />
        </Button>
      </div>
    </>
  );
};
