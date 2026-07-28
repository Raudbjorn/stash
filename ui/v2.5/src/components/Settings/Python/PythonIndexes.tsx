import React, { useEffect, useMemo, useState } from "react";
import { Button, Form, Modal, Table } from "react-bootstrap";
import { FormattedMessage, useIntl } from "react-intl";
import {
  mutateConfigurePythonIndexes,
  mutateRefreshPythonPackageCatalog,
} from "src/core/StashService";
import {
  PythonCatalogStatusDataFragment,
  PythonIndexDataFragment,
  PythonIndexInput,
} from "src/core/generated-graphql";
import { useToast } from "src/hooks/Toast";
import { StartPythonJob } from "./types";

interface PythonIndexesProps {
  indexes: PythonIndexDataFragment[];
  catalogs: PythonCatalogStatusDataFragment[];
  busy: boolean;
  onStartJob: StartPythonJob;
}

type EditableIndex = PythonIndexInput & { original?: number };

function validateIndex(index: EditableIndex, indexes: PythonIndexInput[]) {
  if (!/^[A-Za-z0-9][A-Za-z0-9._-]*$/.test(index.name.trim())) {
    return false;
  }
  try {
    const parsed = new URL(index.url);
    if (
      !["http:", "https:"].includes(parsed.protocol) ||
      !!parsed.username ||
      !!parsed.password ||
      !!parsed.search ||
      !!parsed.hash
    ) {
      return false;
    }
  } catch {
    return false;
  }
  return !indexes.some(
    (candidate, position) =>
      position !== index.original &&
      (candidate.name.toLowerCase() === index.name.trim().toLowerCase() ||
        candidate.url.toLowerCase().replace(/\/+$/, "") ===
          index.url.trim().toLowerCase().replace(/\/+$/, ""))
  );
}

export const PythonIndexes: React.FC<PythonIndexesProps> = ({
  indexes,
  catalogs,
  busy,
  onStartJob,
}) => {
  const intl = useIntl();
  const Toast = useToast();
  const [values, setValues] = useState<PythonIndexInput[]>(indexes);
  const [editing, setEditing] = useState<EditableIndex>();
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    setValues(indexes.map(({ name, url, default: isDefault }) => ({
      name,
      url,
      default: isDefault,
    })));
  }, [indexes]);

  const catalogByName = useMemo(
    () => new Map(catalogs.map((catalog) => [catalog.index, catalog])),
    [catalogs]
  );

  function move(position: number, offset: number) {
    const next = [...values];
    const target = position + offset;
    [next[position], next[target]] = [next[target], next[position]];
    setValues(next);
  }

  function setDefault(position: number) {
    setValues(values.map((index, n) => ({ ...index, default: n === position })));
  }

  function remove(position: number) {
    setValues(values.filter((_index, n) => n !== position));
  }

  function applyEdit() {
    if (!editing || !validateIndex(editing, values)) return;
    const value: PythonIndexInput = {
      name: editing.name.trim(),
      url: editing.url.trim(),
      default: editing.default,
    };
    if (editing.original === undefined) {
      setValues([...values, value]);
    } else {
      const next = [...values];
      next[editing.original] = value;
      setValues(next);
    }
    setEditing(undefined);
  }

  async function save() {
    try {
      setSaving(true);
      await mutateConfigurePythonIndexes(values);
    } catch (error) {
      Toast.error(error);
    } finally {
      setSaving(false);
    }
  }

  async function refresh(index?: string) {
    try {
      const result = await mutateRefreshPythonPackageCatalog(index);
      const id = result.data?.refreshPythonPackageCatalog;
      if (id) onStartJob(id, "catalogs");
    } catch (error) {
      Toast.error(error);
    }
  }

  const hasDefault = values.filter((index) => index.default).length === 1;
  const canSave = values.length > 0 && hasDefault && !busy && !saving;

  return (
    <>
      <div className="python-toolbar">
        <Button
          variant="secondary"
          disabled={busy}
          onClick={() => setEditing({ name: "", url: "", default: false })}
        >
          <FormattedMessage id="config.python.add_index" />
        </Button>
        <Button variant="secondary" disabled={busy} onClick={() => refresh()}>
          <FormattedMessage id="config.python.refresh_catalogs" />
        </Button>
        <Button disabled={!canSave} onClick={save}>
          <FormattedMessage id="actions.save" />
        </Button>
      </div>
      <div className="python-table-wrap">
        <Table responsive hover>
          <thead>
            <tr>
              <th><FormattedMessage id="config.python.default_index" /></th>
              <th><FormattedMessage id="config.python.package_index" /></th>
              <th><FormattedMessage id="config.python.catalog" /></th>
              <th><FormattedMessage id="config.python.actions" /></th>
            </tr>
          </thead>
          <tbody>
            {values.map((index, position) => {
              const catalog = catalogByName.get(index.name);
              return (
                <tr key={`${index.name}-${position}`}>
                  <td>
                    <Form.Check
                      type="radio"
                      id={`python-index-default-${position}`}
                      name="python-default-index"
                      checked={index.default}
                      onChange={() => setDefault(position)}
                      aria-label={intl.formatMessage({ id: "config.python.default_index" })}
                      disabled={busy}
                    />
                  </td>
                  <td>
                    <strong>{index.name}</strong>
                    <div className="python-mono">{index.url}</div>
                  </td>
                  <td>
                    {catalog ? (
                      <>
                        <span>{catalog.projectCount}</span>
                        {catalog.stale && (
                          <span className="text-warning">
                            {" · "}
                            <FormattedMessage id="config.python.stale" />
                          </span>
                        )}
                        {catalog.error && (
                          <div className="text-danger">{catalog.error}</div>
                        )}
                      </>
                    ) : (
                      <FormattedMessage id="config.python.not_refreshed" />
                    )}
                  </td>
                  <td className="python-index-actions">
                    <Button
                      size="sm"
                      variant="secondary"
                      disabled={busy || position === 0}
                      onClick={() => move(position, -1)}
                      aria-label={intl.formatMessage({ id: "config.python.move_up" })}
                    >↑</Button>
                    <Button
                      size="sm"
                      variant="secondary"
                      disabled={busy || position === values.length - 1}
                      onClick={() => move(position, 1)}
                      aria-label={intl.formatMessage({ id: "config.python.move_down" })}
                    >↓</Button>
                    <Button
                      size="sm"
                      variant="secondary"
                      disabled={busy}
                      onClick={() => setEditing({ ...index, original: position })}
                    >
                      <FormattedMessage id="actions.edit" />
                    </Button>
                    <Button
                      size="sm"
                      variant="danger"
                      disabled={busy || index.default || values.length === 1}
                      onClick={() => remove(position)}
                    >
                      <FormattedMessage id="actions.delete" />
                    </Button>
                    <Button
                      size="sm"
                      variant="secondary"
                      disabled={busy}
                      onClick={() => refresh(index.name)}
                    >
                      <FormattedMessage id="actions.refresh" />
                    </Button>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </Table>
      </div>

      <Modal show={!!editing} onHide={() => setEditing(undefined)}>
        <Modal.Header closeButton>
          <Modal.Title><FormattedMessage id="config.python.package_index" /></Modal.Title>
        </Modal.Header>
        <Modal.Body>
          <Form.Group controlId="python-index-name">
            <Form.Label><FormattedMessage id="name" /></Form.Label>
            <Form.Control
              value={editing?.name ?? ""}
              onChange={(event) =>
                setEditing((current) => current && ({ ...current, name: event.currentTarget.value }))
              }
            />
          </Form.Group>
          <Form.Group controlId="python-index-url">
            <Form.Label>URL</Form.Label>
            <Form.Control
              value={editing?.url ?? ""}
              onChange={(event) =>
                setEditing((current) => current && ({ ...current, url: event.currentTarget.value }))
              }
            />
          </Form.Group>
          {editing && !validateIndex(editing, values) && (
            <div className="text-danger">
              <FormattedMessage id="config.python.invalid_index" />
            </div>
          )}
        </Modal.Body>
        <Modal.Footer>
          <Button variant="secondary" onClick={() => setEditing(undefined)}>
            <FormattedMessage id="actions.cancel" />
          </Button>
          <Button disabled={!editing || !validateIndex(editing, values)} onClick={applyEdit}>
            <FormattedMessage id="actions.save" />
          </Button>
        </Modal.Footer>
      </Modal>
    </>
  );
};
