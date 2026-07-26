import React, { useState } from "react";
import { Button, Form, Modal, Table, FormControl } from "react-bootstrap";
import { FormattedMessage, useIntl } from "react-intl";
import * as GQL from "src/core/generated-graphql";
import { useToast } from "src/hooks/Toast";
import { ScanOptions } from "./ScanOptions";
import { LoadingIndicator } from "src/components/Shared/LoadingIndicator";

const DAYS_OF_WEEK = [
  { value: "daily", label: "Every day" },
  { value: "monday", label: "Monday" },
  { value: "tuesday", label: "Tuesday" },
  { value: "wednesday", label: "Wednesday" },
  { value: "thursday", label: "Thursday" },
  { value: "friday", label: "Friday" },
  { value: "saturday", label: "Saturday" },
  { value: "sunday", label: "Sunday" },
];

function formatSpec(spec: string): string {
  const [day, time] = spec.split("@");
  const dayLabel = DAYS_OF_WEEK.find((d) => d.value === day)?.label ?? day;
  return `${dayLabel} at ${time}`;
}

function parseSpec(day: string, hour: string, minute: string): string {
  const hh = hour.padStart(2, "0");
  const mm = minute.padStart(2, "0");
  return `${day}@${hh}:${mm}`;
}

function specToFields(spec: string): {
  day: string;
  hour: string;
  minute: string;
} {
  const [day, time] = spec.split("@");
  const [hour, minute] = (time ?? "00:00").split(":");
  return { day: day ?? "daily", hour: hour ?? "00", minute: minute ?? "00" };
}

const defaultScanOptions: GQL.ScanMetadataOptionsInput = {
  rescan: false,
  scanGenerateCovers: true,
  scanGeneratePreviews: false,
  scanGenerateImagePreviews: false,
  scanGenerateSprites: false,
  scanGeneratePhashes: false,
  scanGenerateImagePhashes: false,
  scanGenerateThumbnails: false,
  scanGenerateClipPreviews: false,
};

interface IScheduleFormProps {
  initial?: GQL.ScanScheduleDataFragment;
  onSave: (
    values: Omit<GQL.ScanScheduleCreateInput, "scanOptions"> & {
      scanOptions: GQL.ScanMetadataOptionsInput;
    }
  ) => void;
  onCancel: () => void;
  saving: boolean;
}

const ScheduleForm: React.FC<IScheduleFormProps> = ({
  initial,
  onSave,
  onCancel,
  saving,
}) => {
  const fields = initial
    ? specToFields(initial.spec)
    : { day: "daily", hour: "02", minute: "00" };

  const [name, setName] = useState(initial?.name ?? "");
  const [day, setDay] = useState(fields.day);
  const [hour, setHour] = useState(fields.hour);
  const [minute, setMinute] = useState(fields.minute);
  const [repeat, setRepeat] = useState(initial?.repeat ?? true);
  const [enabled, setEnabled] = useState(initial?.enabled ?? true);
  const [scanOptions, setScanOptions] = useState<GQL.ScanMetadataOptionsInput>(
    initial?.scanOptions
      ? {
          rescan: initial.scanOptions.rescan,
          scanGenerateCovers: initial.scanOptions.scanGenerateCovers,
          scanGeneratePreviews: initial.scanOptions.scanGeneratePreviews,
          scanGenerateImagePreviews:
            initial.scanOptions.scanGenerateImagePreviews,
          scanGenerateSprites: initial.scanOptions.scanGenerateSprites,
          scanGeneratePhashes: initial.scanOptions.scanGeneratePhashes,
          scanGenerateImagePhashes:
            initial.scanOptions.scanGenerateImagePhashes ?? false,
          scanGenerateThumbnails: initial.scanOptions.scanGenerateThumbnails,
          scanGenerateClipPreviews:
            initial.scanOptions.scanGenerateClipPreviews,
        }
      : { ...defaultScanOptions }
  );

  const hours = Array.from({ length: 24 }, (_, i) =>
    i.toString().padStart(2, "0")
  );
  const minutes = Array.from({ length: 60 }, (_, i) =>
    i.toString().padStart(2, "0")
  );

  function handleSave() {
    onSave({
      name,
      spec: parseSpec(day, hour, minute),
      repeat,
      enabled,
      scanOptions,
    });
  }

  // ScanOptions expects GQL.ScanMetadataInput; cast via compatible shape
  const scanInputProxy = scanOptions as GQL.ScanMetadataInput;

  return (
    <>
      <Modal.Body>
        <Form>
          <Form.Group className="mb-3">
            <Form.Label>
              <FormattedMessage id="name" defaultMessage="Name" />
            </Form.Label>
            <Form.Control
              type="text"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="e.g. Nightly Scan"
            />
          </Form.Group>

          <Form.Group className="mb-3">
            <Form.Label>
              <FormattedMessage id="schedule" defaultMessage="Schedule" />
            </Form.Label>
            <div className="d-flex gap-2 align-items-center flex-wrap">
              <FormControl
                as="select"
                value={day}
                onChange={(e: React.ChangeEvent<HTMLSelectElement>) =>
                  setDay(e.target.value)
                }
                style={{ width: "auto" }}
              >
                {DAYS_OF_WEEK.map((d) => (
                  <option key={d.value} value={d.value}>
                    {d.label}
                  </option>
                ))}
              </FormControl>
              <span>
                <FormattedMessage id="at" defaultMessage="at" />
              </span>
              <FormControl
                as="select"
                value={hour}
                onChange={(e: React.ChangeEvent<HTMLSelectElement>) =>
                  setHour(e.target.value)
                }
                style={{ width: "auto" }}
              >
                {hours.map((h) => (
                  <option key={h} value={h}>
                    {h}
                  </option>
                ))}
              </FormControl>
              <span>:</span>
              <FormControl
                as="select"
                value={minute}
                onChange={(e: React.ChangeEvent<HTMLSelectElement>) =>
                  setMinute(e.target.value)
                }
                style={{ width: "auto" }}
              >
                {minutes.map((m) => (
                  <option key={m} value={m}>
                    {m}
                  </option>
                ))}
              </FormControl>
            </div>
          </Form.Group>

          <Form.Check
            id="schedule-repeat"
            type="checkbox"
            label={
              <FormattedMessage id="repeat" defaultMessage="Repeat weekly" />
            }
            checked={repeat}
            onChange={(e) => setRepeat(e.target.checked)}
            className="mb-3"
          />

          <Form.Check
            id="schedule-enabled"
            type="checkbox"
            label={<FormattedMessage id="enabled" defaultMessage="Enabled" />}
            checked={enabled}
            onChange={(e) => setEnabled(e.target.checked)}
            className="mb-3"
          />

          <hr />
          <h6>
            <FormattedMessage id="scan_options" defaultMessage="Scan Options" />
          </h6>
          <ScanOptions
            options={scanInputProxy}
            setOptions={(v) =>
              setScanOptions(v as GQL.ScanMetadataOptionsInput)
            }
          />
        </Form>
      </Modal.Body>
      <Modal.Footer>
        <Button variant="secondary" onClick={onCancel} disabled={saving}>
          <FormattedMessage id="actions.cancel" defaultMessage="Cancel" />
        </Button>
        <Button
          variant="primary"
          onClick={handleSave}
          disabled={saving || !name.trim()}
        >
          {saving ? (
            <FormattedMessage id="saving" defaultMessage="Saving…" />
          ) : (
            <FormattedMessage id="actions.save" defaultMessage="Save" />
          )}
        </Button>
      </Modal.Footer>
    </>
  );
};

export const ScheduledScans: React.FC = () => {
  const intl = useIntl();
  const Toast = useToast();

  const { data, loading, refetch } = GQL.useScanSchedulesQuery();
  const [createSchedule, { loading: creating }] =
    GQL.useScanScheduleCreateMutation();
  const [updateSchedule, { loading: updating }] =
    GQL.useScanScheduleUpdateMutation();
  const [destroySchedule] = GQL.useScanScheduleDestroyMutation();

  const [showModal, setShowModal] = useState(false);
  const [editing, setEditing] = useState<
    GQL.ScanScheduleDataFragment | undefined
  >(undefined);

  const schedules = data?.scanSchedules ?? [];

  function openCreate() {
    setEditing(undefined);
    setShowModal(true);
  }

  function openEdit(s: GQL.ScanScheduleDataFragment) {
    setEditing(s);
    setShowModal(true);
  }

  async function handleSave(
    values: Omit<GQL.ScanScheduleCreateInput, "scanOptions"> & {
      scanOptions: GQL.ScanMetadataOptionsInput;
    }
  ) {
    try {
      if (editing) {
        await updateSchedule({
          variables: { input: { id: editing.id, ...values } },
        });
        Toast.success(
          intl.formatMessage(
            { id: "toast.updated_entity", defaultMessage: "Updated {entity}" },
            { entity: values.name }
          )
        );
      } else {
        await createSchedule({ variables: { input: values } });
        Toast.success(
          intl.formatMessage(
            { id: "toast.created_entity", defaultMessage: "Created {entity}" },
            { entity: values.name }
          )
        );
      }
      setShowModal(false);
      refetch();
    } catch (e) {
      Toast.error(e);
    }
  }

  async function handleDestroy(id: string, name: string) {
    if (!window.confirm(`Delete schedule "${name}"?`)) return;
    try {
      await destroySchedule({ variables: { id } });
      Toast.success(
        intl.formatMessage(
          { id: "toast.deleted_entity", defaultMessage: "Deleted {entity}" },
          { entity: name }
        )
      );
      refetch();
    } catch (e) {
      Toast.error(e);
    }
  }

  if (loading) return <LoadingIndicator />;

  const saving = creating || updating;

  return (
    <div className="scheduled-scans">
      <div className="d-flex justify-content-between align-items-center mb-3">
        <h5 className="mb-0">
          <FormattedMessage
            id="config.tasks.scheduled_scans"
            defaultMessage="Scheduled Scans"
          />
        </h5>
        <Button variant="primary" size="sm" onClick={openCreate}>
          <FormattedMessage
            id="actions.add_schedule"
            defaultMessage="Add Schedule"
          />
        </Button>
      </div>

      {schedules.length === 0 ? (
        <p className="text-muted">
          <FormattedMessage
            id="config.tasks.no_schedules"
            defaultMessage="No scan schedules configured."
          />
        </p>
      ) : (
        <Table size="sm" responsive>
          <thead>
            <tr>
              <th>
                <FormattedMessage id="name" defaultMessage="Name" />
              </th>
              <th>
                <FormattedMessage id="schedule" defaultMessage="Schedule" />
              </th>
              <th>
                <FormattedMessage id="repeat" defaultMessage="Repeat" />
              </th>
              <th>
                <FormattedMessage id="next_run" defaultMessage="Next Run" />
              </th>
              <th>
                <FormattedMessage id="enabled" defaultMessage="Enabled" />
              </th>
              <th />
            </tr>
          </thead>
          <tbody>
            {schedules.map((s) => (
              <tr key={s.id} className={s.enabled ? "" : "text-muted"}>
                <td>{s.name}</td>
                <td>{formatSpec(s.spec)}</td>
                <td>{s.repeat ? "Yes" : "No"}</td>
                <td>
                  {s.enabled && s.nextRunAt
                    ? new Date(s.nextRunAt).toLocaleString()
                    : "—"}
                </td>
                <td>{s.enabled ? "✓" : "—"}</td>
                <td className="text-end">
                  <Button
                    variant="outline-secondary"
                    size="sm"
                    className="me-1"
                    onClick={() => openEdit(s)}
                  >
                    <FormattedMessage id="actions.edit" defaultMessage="Edit" />
                  </Button>
                  <Button
                    variant="outline-danger"
                    size="sm"
                    onClick={() => handleDestroy(s.id, s.name)}
                  >
                    <FormattedMessage
                      id="actions.delete"
                      defaultMessage="Delete"
                    />
                  </Button>
                </td>
              </tr>
            ))}
          </tbody>
        </Table>
      )}

      <Modal
        show={showModal}
        onHide={() => setShowModal(false)}
        size="lg"
        backdrop="static"
      >
        <Modal.Header closeButton>
          <Modal.Title>
            {editing ? (
              <FormattedMessage
                id="actions.edit_schedule"
                defaultMessage="Edit Schedule"
              />
            ) : (
              <FormattedMessage
                id="actions.add_schedule"
                defaultMessage="Add Schedule"
              />
            )}
          </Modal.Title>
        </Modal.Header>
        <ScheduleForm
          initial={editing}
          onSave={handleSave}
          onCancel={() => setShowModal(false)}
          saving={saving}
        />
      </Modal>
    </div>
  );
};
