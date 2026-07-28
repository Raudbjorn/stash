import React, { useEffect, useMemo, useState } from "react";
import { Button, Form, Table } from "react-bootstrap";
import { FormattedMessage, useIntl } from "react-intl";
import { AlertModal } from "src/components/Shared/Alert";
import { ClearableInput } from "src/components/Shared/ClearableInput";
import { LoadingIndicator } from "src/components/Shared/LoadingIndicator";
import {
  mutateInstallPythonPackages,
  mutateRefreshPythonPackageCatalog,
  mutateUninstallPythonPackages,
  mutateUpdatePythonPackages,
} from "src/core/StashService";
import {
  PythonCatalogStatusDataFragment,
  PythonIndexDataFragment,
  usePythonPackagesQuery,
  useSearchPythonPackagesQuery,
} from "src/core/generated-graphql";
import { useToast } from "src/hooks/Toast";
import { StartPythonJob } from "./types";

interface PythonPackagesProps {
  indexes: PythonIndexDataFragment[];
  catalogs: PythonCatalogStatusDataFragment[];
  managerAvailable: boolean;
  managedEnvironment: boolean;
  busy: boolean;
  onStartJob: StartPythonJob;
}

type PendingPackageAction = { operation: "update" | "uninstall"; name?: string };

export const PythonPackages: React.FC<PythonPackagesProps> = ({
  indexes,
  catalogs,
  managerAvailable,
  managedEnvironment,
  busy,
  onStartJob,
}) => {
  const intl = useIntl();
  const Toast = useToast();
  const [filter, setFilter] = useState("");
  const [outdated, setOutdated] = useState(false);
  const [pending, setPending] = useState<PendingPackageAction>();
  const [adding, setAdding] = useState(false);
  const [search, setSearch] = useState("");
  const [debouncedSearch, setDebouncedSearch] = useState("");
  const [searchIndex, setSearchIndex] = useState<string>();

  const packageQuery = usePythonPackagesQuery({
    variables: { outdated },
    skip: !managerAvailable,
    notifyOnNetworkStatusChange: true,
  });

  useEffect(() => {
    const timer = window.setTimeout(() => setDebouncedSearch(search.trim()), 250);
    return () => window.clearTimeout(timer);
  }, [search]);

  const searchQuery = useSearchPythonPackagesQuery({
    variables: { query: debouncedSearch, index: searchIndex, limit: 50 },
    skip: !adding || debouncedSearch.length < 2,
  });

  const packages = useMemo(() => {
    const term = filter.trim().toLowerCase();
    return (packageQuery.data?.pythonPackages ?? []).filter(
      (pkg) => !term || pkg.name.toLowerCase().includes(term)
    );
  }, [filter, packageQuery.data]);

  const availableNames = useMemo(() => {
    const results = [...(searchQuery.data?.searchPythonPackages ?? [])];
    if (
      /^[A-Za-z0-9](?:[A-Za-z0-9._-]*[A-Za-z0-9])?$/.test(
        debouncedSearch
      ) &&
      !results.some(
        (result) => result.name.toLowerCase() === debouncedSearch.toLowerCase()
      )
    ) {
      results.unshift({
        __typename: "PythonPackageSearchResult",
        name: debouncedSearch,
        indexes: [],
      });
    }
    return results;
  }, [debouncedSearch, searchQuery.data]);

  async function openAddPackages() {
    setAdding(true);
    if (!busy && catalogs.some((catalog) => catalog.stale)) {
      try {
        const result = await mutateRefreshPythonPackageCatalog();
        const id = result.data?.refreshPythonPackageCatalog;
        if (id) onStartJob(id, "catalogs");
      } catch (error) {
        Toast.error(error);
      }
    }
  }

  async function install(name: string) {
    try {
      const result = await mutateInstallPythonPackages([name]);
      const id = result.data?.installPythonPackages;
      if (id) onStartJob(id, "packages");
    } catch (error) {
      Toast.error(error);
    }
  }

  async function confirmAction() {
    if (!pending) return;
    try {
      let id: string | undefined;
      if (pending.operation === "update") {
        const result = await mutateUpdatePythonPackages(
          pending.name ? [pending.name] : undefined
        );
        id = result.data?.updatePythonPackages;
      } else {
        const result = await mutateUninstallPythonPackages([
          pending.name ?? "",
        ]);
        id = result.data?.uninstallPythonPackages;
      }
      if (id) onStartJob(id, "packages");
      setPending(undefined);
    } catch (error) {
      Toast.error(error);
    }
  }

  return (
    <>
      {!managedEnvironment && (
        <div className="alert alert-warning m-3">
          <FormattedMessage id="config.python.packages_read_only" />
        </div>
      )}
      <div className="python-toolbar">
        <ClearableInput
          value={filter}
          setValue={setFilter}
          placeholder={intl.formatMessage({ id: "filter" })}
        />
        <Button
          variant="secondary"
          disabled={busy || !managerAvailable}
          onClick={() => setOutdated(true)}
        >
          <FormattedMessage id="config.python.check_updates" />
        </Button>
        {packages.some((pkg) => pkg.latestVersion) && (
          <Button
            disabled={busy || !managedEnvironment}
            onClick={() => setPending({ operation: "update" })}
          >
            <FormattedMessage id="config.python.update_all" />
          </Button>
        )}
      </div>
      {packageQuery.loading && <LoadingIndicator />}
      {packageQuery.error && (
        <div className="text-danger content">{packageQuery.error.message}</div>
      )}
      <div className="python-table-wrap">
        <Table responsive hover>
          <thead>
            <tr>
              <th><FormattedMessage id="config.python.package" /></th>
              <th><FormattedMessage id="config.python.version" /></th>
              <th><FormattedMessage id="config.python.actions" /></th>
            </tr>
          </thead>
          <tbody>
            {packages.map((pkg) => (
              <tr key={pkg.name}>
                <td>{pkg.name}</td>
                <td>
                  {pkg.version}
                  {pkg.latestVersion && (
                    <span className="python-update-version">→ {pkg.latestVersion}</span>
                  )}
                </td>
                <td>
                  {pkg.latestVersion && (
                    <Button
                      size="sm"
                      disabled={busy || !managedEnvironment}
                      onClick={() => setPending({ operation: "update", name: pkg.name })}
                    >
                      <FormattedMessage id="package_manager.update" />
                    </Button>
                  )}
                  <Button
                    size="sm"
                    variant="danger"
                    disabled={busy || !managedEnvironment}
                    onClick={() => setPending({ operation: "uninstall", name: pkg.name })}
                  >
                    <FormattedMessage id="package_manager.uninstall" />
                  </Button>
                </td>
              </tr>
            ))}
          </tbody>
        </Table>
      </div>

      <div className="python-add-packages">
        <Button
          variant="secondary"
          onClick={adding ? () => setAdding(false) : openAddPackages}
          disabled={!managerAvailable}
        >
          <FormattedMessage id="config.python.add_packages" />
        </Button>
        {adding && (
          <div className="python-add-body">
            <div className="python-toolbar">
              <ClearableInput
                value={search}
                setValue={setSearch}
                placeholder={intl.formatMessage({ id: "config.python.search_packages" })}
              />
              <Form.Label htmlFor="python-search-index" className="sr-only">
                <FormattedMessage id="config.python.package_index" />
              </Form.Label>
              <Form.Control
                id="python-search-index"
                as="select"
                value={searchIndex ?? ""}
                onChange={(event) =>
                  setSearchIndex(event.currentTarget.value || undefined)
                }
              >
                <option value="">
                  {intl.formatMessage({ id: "config.python.all_indexes" })}
                </option>
                {indexes.map((index) => (
                  <option key={index.name} value={index.name}>{index.name}</option>
                ))}
              </Form.Control>
            </div>
            {searchQuery.loading && <LoadingIndicator />}
            {searchQuery.error && (
              <div className="text-danger">{searchQuery.error.message}</div>
            )}
            <ul className="python-search-results">
              {availableNames.map((result) => (
                <li key={result.name}>
                  <span>
                    <strong>{result.name}</strong>
                    {result.indexes.length > 0 && (
                      <small>{result.indexes.join(", ")}</small>
                    )}
                  </span>
                  <Button
                    size="sm"
                    disabled={busy || !managedEnvironment}
                    onClick={() => install(result.name)}
                  >
                    <FormattedMessage id="package_manager.install" />
                  </Button>
                </li>
              ))}
            </ul>
          </div>
        )}
      </div>

      <AlertModal
        show={!!pending}
        text={
          <FormattedMessage
            id={
              pending?.operation === "uninstall"
                ? "config.python.uninstall_confirmation"
                : "config.python.update_confirmation"
            }
          />
        }
        confirmVariant={pending?.operation === "uninstall" ? "danger" : "primary"}
        onConfirm={confirmAction}
        onCancel={() => setPending(undefined)}
      />
    </>
  );
};
