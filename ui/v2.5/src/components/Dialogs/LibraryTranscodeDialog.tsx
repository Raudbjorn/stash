import React, { useState } from "react";
import { Form } from "react-bootstrap";
import {
  faExclamationTriangle,
  faTasks,
} from "@fortawesome/free-solid-svg-icons";
import { FormattedMessage, useIntl } from "react-intl";
import * as GQL from "src/core/generated-graphql";
import {
  mutateMetadataLibraryTranscode,
  mutateMetadataScoreTranscodeBenefit,
} from "src/core/StashService";
import { ModalComponent } from "../Shared/Modal";
import { useToast } from "src/hooks/Toast";

interface ILibraryTranscodeDialog {
  selected: GQL.SlimSceneDataFragment[];
  onClose: () => void;
}

export const LibraryTranscodeDialog: React.FC<ILibraryTranscodeDialog> = ({
  selected,
  onClose,
}) => {
  const intl = useIntl();
  const Toast = useToast();
  const sceneIDs = selected.map((s) => s.id);
  const [profile, setProfile] = useState<GQL.LibraryTranscodeProfile>(
    GQL.LibraryTranscodeProfile.Hq_480
  );

  async function onSubmit() {
    if (sceneIDs.length === 0) {
      return;
    }
    try {
      await mutateMetadataLibraryTranscode({
        sceneIDs,
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
      onClose();
    }
  }

  return (
    <ModalComponent
      show
      icon={faExclamationTriangle}
      header={intl.formatMessage({ id: "actions.library_transcode" })}
      accept={{
        onClick: onSubmit,
        text: intl.formatMessage({ id: "actions.library_transcode" }),
        variant: "danger",
      }}
      cancel={{
        onClick: () => onClose(),
        text: intl.formatMessage({ id: "actions.cancel" }),
        variant: "secondary",
      }}
    >
      <div className="alert alert-danger text-break">
        <FormattedMessage id="dialogs.library_transcode_confirm" />
        <ul className="mt-2 mb-0">
          {selected.slice(0, 5).map((s) => {
            const path = s.files?.[0]?.path;
            return path ? <li key={s.id}>{path}</li> : null;
          })}
          {selected.length > 5 && (
            <li>
              <FormattedMessage
                id="dialogs.delete_object_overflow"
                values={{
                  count: selected.length - 5,
                  singularEntity: intl.formatMessage({ id: "scene" }),
                  pluralEntity: intl.formatMessage({ id: "scenes" }),
                }}
              />
            </li>
          )}
        </ul>
      </div>
      <Form>
        <Form.Group>
          <Form.Label>
            <FormattedMessage id="dialogs.library_transcode_profile" />
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
                id: "library_transcode_profile.HQ_480",
              })}
            </option>
            <option value={GQL.LibraryTranscodeProfile.Lq_480}>
              {intl.formatMessage({
                id: "library_transcode_profile.LQ_480",
              })}
            </option>
            <option value={GQL.LibraryTranscodeProfile.Max_360}>
              {intl.formatMessage({
                id: "library_transcode_profile.MAX_360",
              })}
            </option>
          </Form.Control>
        </Form.Group>
      </Form>
    </ModalComponent>
  );
};

interface IScoreTranscodeBenefitDialog {
  onClose: () => void;
}

export const ScoreTranscodeBenefitDialog: React.FC<IScoreTranscodeBenefitDialog> =
  ({ onClose }) => {
    const intl = useIntl();
    const Toast = useToast();

    async function onSubmit() {
      try {
        await mutateMetadataScoreTranscodeBenefit({ sceneIDs: [] });
        Toast.success(
          intl.formatMessage(
            { id: "config.tasks.added_job_to_queue" },
            {
              operation_name: intl.formatMessage({
                id: "actions.score_transcode_benefit",
              }),
            }
          )
        );
      } catch (e) {
        Toast.error(e);
      } finally {
        onClose();
      }
    }

    return (
      <ModalComponent
        show
        icon={faTasks}
        header={intl.formatMessage({ id: "actions.score_transcode_benefit" })}
        accept={{
          onClick: onSubmit,
          text: intl.formatMessage({ id: "actions.score_transcode_benefit" }),
          variant: "danger",
        }}
        cancel={{
          onClick: () => onClose(),
          text: intl.formatMessage({ id: "actions.cancel" }),
          variant: "secondary",
        }}
      >
        <div className="alert alert-danger text-break">
          <FormattedMessage id="dialogs.score_transcode_benefit_all" />
        </div>
      </ModalComponent>
    );
  };

export default LibraryTranscodeDialog;
