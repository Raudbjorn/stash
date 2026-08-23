import React, { useState } from "react";
import { Form } from "react-bootstrap";
import { faExclamationTriangle } from "@fortawesome/free-solid-svg-icons";
import { FormattedMessage, useIntl } from "react-intl";
import * as GQL from "src/core/generated-graphql";
import { mutateMetadataLibraryTranscode } from "src/core/StashService";
import { ModalComponent } from "../Shared/Modal";
import { useToast } from "src/hooks/Toast";

interface ILibraryTranscodeDialog {
  selectedIds: string[];
  onClose: () => void;
}

export const LibraryTranscodeDialog: React.FC<ILibraryTranscodeDialog> = ({
  selectedIds,
  onClose,
}) => {
  const intl = useIntl();
  const Toast = useToast();
  const [profile, setProfile] = useState<GQL.LibraryTranscodeProfile>(
    GQL.LibraryTranscodeProfile.Hq_480
  );
  const [running, setRunning] = useState(false);

  async function onSubmit() {
    if (selectedIds.length === 0) {
      return;
    }
    setRunning(true);
    try {
      await mutateMetadataLibraryTranscode({
        sceneIDs: selectedIds,
        profile,
      });
      Toast.success(
        intl.formatMessage(
          { id: "config.tasks.added_job_to_queue" },
          {
            operation_name: intl.formatMessage({
              id: "actions.library_transcode",
            }),
          }
        )
      );
    } catch (e) {
      Toast.error(e);
    } finally {
      setRunning(false);
      onClose();
    }
  }

  return (
    <ModalComponent
      show
      icon={faExclamationTriangle}
      header={intl.formatMessage({ id: "dialogs.library_transcode.title" })}
      accept={{
        onClick: onSubmit,
        text: intl.formatMessage({ id: "dialogs.library_transcode.confirm" }),
        variant: "danger",
      }}
      cancel={{
        onClick: () => onClose(),
        text: intl.formatMessage({ id: "actions.cancel" }),
        variant: "secondary",
      }}
      isRunning={running}
      disabled={selectedIds.length === 0}
    >
      <Form>
        <p>
          <FormattedMessage
            id="dialogs.library_transcode.desc"
            values={{ count: selectedIds.length }}
          />
        </p>
        <Form.Group>
          <Form.Label>
            <FormattedMessage id="dialogs.library_transcode.profile" />
          </Form.Label>
          <Form.Control
            as="select"
            className="input-control"
            value={profile}
            onChange={(e) =>
              setProfile(e.currentTarget.value as GQL.LibraryTranscodeProfile)
            }
          >
            <option value={GQL.LibraryTranscodeProfile.Hq_480}>
              {intl.formatMessage({
                id: "dialogs.library_transcode.profiles.HQ_480",
              })}
            </option>
            <option value={GQL.LibraryTranscodeProfile.Lq_480}>
              {intl.formatMessage({
                id: "dialogs.library_transcode.profiles.LQ_480",
              })}
            </option>
            <option value={GQL.LibraryTranscodeProfile.Max_360}>
              {intl.formatMessage({
                id: "dialogs.library_transcode.profiles.MAX_360",
              })}
            </option>
          </Form.Control>
        </Form.Group>
      </Form>
    </ModalComponent>
  );
};

export default LibraryTranscodeDialog;
