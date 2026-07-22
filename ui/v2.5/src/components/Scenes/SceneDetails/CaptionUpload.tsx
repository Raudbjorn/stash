import React, { useState, useRef } from "react";
import { Button, Form } from "react-bootstrap";
import { FormattedMessage, useIntl } from "react-intl";
import { useSceneUploadCaption } from "src/core/StashService";
import { useToast } from "src/hooks/Toast";
import * as GQL from "src/core/generated-graphql";
import { faUpload } from "@fortawesome/free-solid-svg-icons";
import { Icon } from "src/components/Shared/Icon";
import { ModalComponent } from "src/components/Shared/Modal";

interface ICaptionUploadProps {
  scene: GQL.SceneDataFragment;
  onSuccess?: () => void;
}

export const CaptionUpload: React.FC<ICaptionUploadProps> = ({
  scene,
  onSuccess,
}) => {
  const intl = useIntl();
  const Toast = useToast();
  const fileInputRef = useRef<HTMLInputElement>(null);

  const [selectedFile, setSelectedFile] = useState<File | null>(null);
  const [showConfirmModal, setShowConfirmModal] = useState(false);
  const [uploading, setUploading] = useState(false);

  const [uploadCaption] = useSceneUploadCaption();

  // Check if caption with same language already exists
  const checkExistingCaption = (file: File): boolean => {
    const filename = file.name;
    const ext = filename.split(".").pop()?.toLowerCase();

    if (!ext || !["srt", "vtt"].includes(ext)) {
      return false;
    }

    // Extract language code from filename (e.g., subtitle.en.srt -> "en")
    const parts = filename.split(".");
    let languageCode = "en"; // default

    if (parts.length >= 3) {
      const potentialLang = parts[parts.length - 2];
      if (potentialLang.length === 2) {
        languageCode = potentialLang;
      }
    }

    const existing = scene.captions?.find(
      (c) => c.language_code === languageCode && c.caption_type === ext
    );

    return !!existing;
  };

  const handleFileSelect = (event: React.ChangeEvent<HTMLInputElement>) => {
    const file = event.target.files?.[0];
    if (!file) return;

    const ext = file.name.split(".").pop()?.toLowerCase();
    if (!ext || !["srt", "vtt"].includes(ext)) {
      Toast.error(
        intl.formatMessage(
          { id: "errors.invalid_file_type" },
          { type: ".srt or .vtt" }
        )
      );
      return;
    }

    setSelectedFile(file);

    if (checkExistingCaption(file)) {
      setShowConfirmModal(true);
    } else {
      handleUpload(file);
    }
  };

  const handleUpload = async (file: File) => {
    setUploading(true);

    try {
      await uploadCaption({
        variables: {
          input: {
            scene_id: scene.id,
            caption_file: file,
            language_code: null,
          },
        },
      });

      Toast.success(
        intl.formatMessage({ id: "toast.upload_caption_success" })
      );

      if (fileInputRef.current) {
        fileInputRef.current.value = "";
      }
      setSelectedFile(null);

      onSuccess?.();
    } catch (error) {
      Toast.error(error);
    } finally {
      setUploading(false);
    }
  };

  const handleConfirmUpload = () => {
    setShowConfirmModal(false);
    if (selectedFile) {
      handleUpload(selectedFile);
    }
  };

  const handleCancelUpload = () => {
    setShowConfirmModal(false);
    setSelectedFile(null);
    if (fileInputRef.current) {
      fileInputRef.current.value = "";
    }
  };

  return (
    <>
      <div className="caption-upload-section mt-3">
        <Form.Group>
          <Form.Label>
            <FormattedMessage id="captions" />
          </Form.Label>
          <div className="d-flex align-items-center">
            <Form.Control
              type="file"
              accept=".srt,.vtt"
              ref={fileInputRef}
              onChange={handleFileSelect}
              disabled={uploading}
              className="mr-2"
            />
            <Button
              variant="secondary"
              disabled={uploading}
              onClick={() => fileInputRef.current?.click()}
              title={intl.formatMessage({ id: "actions.upload_caption" })}
            >
              <Icon icon={faUpload} />
            </Button>
          </div>
          <Form.Text className="text-muted">
            <FormattedMessage id="caption_upload_help" />
          </Form.Text>

          {scene.captions && scene.captions.length > 0 && (
            <div className="mt-2">
              <small>
                <FormattedMessage id="existing_captions" />:{" "}
                {scene.captions
                  .map((c) => `${c.language_code} (${c.caption_type})`)
                  .join(", ")}
              </small>
            </div>
          )}
        </Form.Group>
      </div>

      <ModalComponent
        show={showConfirmModal}
        onHide={handleCancelUpload}
        header={intl.formatMessage({ id: "caption_exists_header" })}
        accept={{
          text: intl.formatMessage({ id: "actions.overwrite" }),
          variant: "danger",
          onClick: handleConfirmUpload,
        }}
        cancel={{
          text: intl.formatMessage({ id: "actions.cancel" }),
          variant: "secondary",
          onClick: handleCancelUpload,
        }}
      >
        <p>
          <FormattedMessage id="caption_exists_message" />
        </p>
      </ModalComponent>
    </>
  );
};

export default CaptionUpload;
