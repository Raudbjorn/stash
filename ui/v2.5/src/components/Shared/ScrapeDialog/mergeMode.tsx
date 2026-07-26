import React, { useState } from "react";
import { useApolloClient } from "@apollo/client";
import { Button, ButtonGroup, OverlayTrigger, Tooltip } from "react-bootstrap";
import { useIntl } from "react-intl";
import { ScrapeListMergeMode } from "src/core/config";
import * as GQL from "src/core/generated-graphql";
import { useConfigureUISetting } from "src/core/StashService";
import { useConfigurationContextOptional } from "src/hooks/Config";
import { useToast } from "src/hooks/Toast";
import { IHasStoredID, sortStoredIdObjects } from "src/utils/data";
import { ObjectListScrapeResult, ScrapeResult } from "./scrapeResult";

function isValidMergeMode(mode: unknown): mode is ScrapeListMergeMode {
  return mode === "merge" || mode === "overwrite";
}

// scrapeDialogMergeModes is stored as a single map under one config key, so
// concurrent per-field saves are read-modify-write operations on shared
// state. A dialog mounts one hook per list field, and each spreads the map
// before writing its own entry. Serialize the saves through a shared chain
// so each runs after the previous one has written back, and each reads the
// freshest map rather than racing on a stale snapshot.
let mergeModeSaveQueue: Promise<unknown> = Promise.resolve();

function enqueueMergeModeSave(save: () => Promise<unknown>): Promise<unknown> {
  const next = mergeModeSaveQueue.then(save, save);
  // Keep the chain alive even if a save rejects, without surfacing an
  // unhandled rejection on the shared queue itself.
  mergeModeSaveQueue = next.catch(() => undefined);
  return next;
}

// usePersistedMergeMode returns the persisted merge mode for the given field,
// falling back to the given default, and a function to persist a new mode in
// the UI configuration.
function usePersistedMergeMode(
  field: string,
  defaultMode: ScrapeListMergeMode
) {
  const Toast = useToast();
  const context = useConfigurationContextOptional();
  const client = useApolloClient();
  const [saveUISetting] = useConfigureUISetting();

  const mergeModes = context?.configuration?.ui?.scrapeDialogMergeModes;
  const persisted = mergeModes?.[field];
  const initialMode = isValidMergeMode(persisted) ? persisted : defaultMode;

  async function persistMode(mode: ScrapeListMergeMode) {
    try {
      await enqueueMergeModeSave(() => {
        // Read the freshest merge modes inside the queued task, after any
        // preceding save has written back to the cache. The render-time
        // snapshot may be stale, so spreading it would drop other fields'
        // preferences.
        const cached = client.readQuery<GQL.ConfigurationQuery>({
          query: GQL.ConfigurationDocument,
        });
        const latestModes =
          cached?.configuration?.ui?.scrapeDialogMergeModes ?? mergeModes;

        return saveUISetting({
          variables: {
            key: "scrapeDialogMergeModes",
            value: {
              ...latestModes,
              [field]: mode,
            },
          },
        });
      });
    } catch (e) {
      Toast.error(e);
    }
  }

  return { initialMode, persistMode };
}

export interface IMergeModeScrapeResult<R> {
  result: R;
  setResult: React.Dispatch<React.SetStateAction<R>>;
  mergeMode: ScrapeListMergeMode;
  onSetMergeMode: (mode: ScrapeListMergeMode) => void;
}

// useMergeModeResultState manages a scrape result with a persisted merge
// mode. init must return the initial (unmerged) result, plus functions to
// apply and remove the merged-in existing values.
function useMergeModeResultState<V, R extends ScrapeResult<V>>(
  field: string,
  defaultMode: ScrapeListMergeMode,
  init: () => {
    base: R;
    merge: (r: R) => R;
    overwrite: (r: R) => R;
  }
): IMergeModeScrapeResult<R> {
  const { initialMode, persistMode } = usePersistedMergeMode(
    field,
    defaultMode
  );
  const [mode, setMode] = useState(initialMode);
  const [fns] = useState(init);

  const [result, setResult] = useState<R>(() =>
    initialMode === "merge" && fns.base.scraped ? fns.merge(fns.base) : fns.base
  );

  function onSetMergeMode(m: ScrapeListMergeMode) {
    if (m === mode) {
      return;
    }

    setMode(m);
    persistMode(m);
    setResult(m === "merge" ? fns.merge(result) : fns.overwrite(result));
  }

  return { result, setResult, mergeMode: mode, onSetMergeMode };
}

interface IMergeModeListOptions<T, R extends ScrapeResult<T[]>> {
  field: string;
  defaultMode?: ScrapeListMergeMode;
  // creates the initial scrape result, without any merging applied
  createResult: () => R;
  isEqual: (v1: T, v2: T) => boolean;
  // optional sort applied to merged values
  sortValues?: (values: T[]) => T[];
}

// useMergeModeList manages a list scrape result with a persisted merge mode.
// In merge mode, existing values missing from the scraped result are added to
// it. Toggling back to overwrite mode removes them again, leaving any other
// changes intact.
export function useMergeModeList<T, R extends ScrapeResult<T[]>>(
  options: IMergeModeListOptions<T, R>
): IMergeModeScrapeResult<R> {
  const {
    field,
    defaultMode = "overwrite",
    createResult,
    isEqual,
    sortValues,
  } = options;

  return useMergeModeResultState<T[], R>(field, defaultMode, () => {
    const base = createResult();
    const scrapedValues = base.newValue ?? [];

    // capture the existing values missing from the scraped result, so that
    // the merge can be applied and removed after the fact
    const missingExisting = (base.originalValue ?? []).filter(
      (v) => !scrapedValues.some((vv) => isEqual(v, vv))
    );

    function merge(r: R) {
      const current = r.newValue ?? [];
      const missing = missingExisting.filter(
        (v) => !current.some((vv) => isEqual(v, vv))
      );
      if (missing.length === 0) {
        return r;
      }

      const merged = [...missing, ...current];
      return r.cloneWithValue(sortValues ? sortValues(merged) : merged);
    }

    function overwrite(r: R) {
      const current = r.newValue ?? [];
      const filtered = current.filter(
        (v) => !missingExisting.some((vv) => isEqual(v, vv))
      );
      if (filtered.length === current.length) {
        return r;
      }

      return r.cloneWithValue(filtered);
    }

    return { base, merge, overwrite };
  });
}

// useMergeModeStringList manages a string list scrape result with a persisted
// merge mode. Defaults to merge mode to preserve the existing URL behaviour.
export function useMergeModeStringList(
  field: string,
  originalValue: string[] | undefined | null,
  scrapedValue: string[] | undefined | null,
  defaultMode: ScrapeListMergeMode = "merge"
): IMergeModeScrapeResult<ScrapeResult<string[]>> {
  return useMergeModeList({
    field,
    defaultMode,
    createResult: () => new ScrapeResult<string[]>(originalValue, scrapedValue),
    isEqual: (v1: string, v2: string) => v1 === v2,
  });
}

// useMergeModeObjectList manages an object list scrape result with a
// persisted merge mode. Values are compared by stored_id.
export function useMergeModeObjectList<T extends IHasStoredID>(
  field: string,
  originalValue: T[] | undefined | null,
  scrapedValue: T[] | undefined | null,
  defaultMode: ScrapeListMergeMode = "overwrite"
): IMergeModeScrapeResult<ObjectListScrapeResult<T>> {
  return useMergeModeList({
    field,
    defaultMode,
    createResult: () =>
      new ObjectListScrapeResult<T>(
        sortStoredIdObjects(originalValue ?? undefined),
        sortStoredIdObjects(scrapedValue ?? undefined)
      ),
    isEqual: (v1: T, v2: T) => v1.stored_id === v2.stored_id,
    sortValues: (values) => sortStoredIdObjects(values) ?? [],
  });
}

function splitValues(v: string | undefined): string[] {
  return v
    ? v
        .split(",")
        .map((vv) => vv.trim())
        .filter((vv) => vv.length > 0)
    : [];
}

// useMergeModeDelimitedString manages a comma-delimited string scrape result
// (such as performer aliases) with a persisted merge mode. Values are
// compared case-insensitively.
export function useMergeModeDelimitedString(
  field: string,
  originalValue: string | undefined | null,
  scrapedValue: string | undefined | null,
  defaultMode: ScrapeListMergeMode = "overwrite"
): IMergeModeScrapeResult<ScrapeResult<string>> {
  return useMergeModeResultState<string, ScrapeResult<string>>(
    field,
    defaultMode,
    () => {
      const base = new ScrapeResult<string>(originalValue, scrapedValue);

      const isEqual = (v1: string, v2: string) =>
        v1.toLowerCase() === v2.toLowerCase();

      const scrapedValues = splitValues(base.newValue);
      const missingExisting = splitValues(base.originalValue).filter(
        (v) => !scrapedValues.some((vv) => isEqual(v, vv))
      );

      function merge(r: ScrapeResult<string>) {
        const current = splitValues(r.newValue);
        const missing = missingExisting.filter(
          (v) => !current.some((vv) => isEqual(v, vv))
        );
        if (missing.length === 0) {
          return r;
        }

        return r.cloneWithValue([...missing, ...current].join(", "));
      }

      function overwrite(r: ScrapeResult<string>) {
        const current = splitValues(r.newValue);
        const filtered = current.filter(
          (v) => !missingExisting.some((vv) => isEqual(v, vv))
        );
        if (filtered.length === current.length) {
          return r;
        }

        return r.cloneWithValue(filtered.join(", "));
      }

      return { base, merge, overwrite };
    }
  );
}

const mergeModes: ScrapeListMergeMode[] = ["merge", "overwrite"];

const modeMessageIDs: Record<ScrapeListMergeMode, string> = {
  merge: "actions.merge",
  overwrite: "actions.overwrite",
};

const modeTooltipIDs: Record<ScrapeListMergeMode, string> = {
  merge: "dialogs.scrape_results_merge_tooltip",
  overwrite: "dialogs.scrape_results_overwrite_tooltip",
};

export const MergeModeButtons: React.FC<{
  mode: ScrapeListMergeMode;
  onSetMode: (mode: ScrapeListMergeMode) => void;
}> = ({ mode, onSetMode }) => {
  const intl = useIntl();

  return (
    <ButtonGroup className="merge-mode-buttons">
      {mergeModes.map((m) => (
        <OverlayTrigger
          key={m}
          placement="top"
          overlay={
            <Tooltip id={`merge-mode-${m}-tooltip`}>
              {intl.formatMessage({ id: modeTooltipIDs[m] })}
            </Tooltip>
          }
        >
          <Button
            variant="primary"
            active={mode === m}
            size="sm"
            onClick={() => onSetMode(m)}
          >
            {intl.formatMessage({ id: modeMessageIDs[m] })}
          </Button>
        </OverlayTrigger>
      ))}
    </ButtonGroup>
  );
};
