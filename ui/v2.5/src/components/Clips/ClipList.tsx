import React, { useState } from "react";
import { Button } from "react-bootstrap";
import { Link } from "react-router-dom";
import * as GQL from "src/core/generated-graphql";
import { useFindClips } from "src/core/StashService";
import { LoadingIndicator } from "../Shared/LoadingIndicator";
import { ClipCardGrid } from "./ClipCardGrid";
import { ClipCreateModal } from "./ClipCreateModal";

const PER_PAGE = 40;

export const ClipList: React.FC = () => {
  const [page, setPage] = useState(1);
  const [showCreate, setShowCreate] = useState(false);

  const { data, loading, error, refetch } = useFindClips({
    filter: {
      per_page: PER_PAGE,
      page,
      sort: "created_at",
      direction: GQL.SortDirectionEnum.Desc,
    },
  });

  const clips = data?.findClips.clips ?? [];
  const totalCount = data?.findClips.count ?? 0;
  const maxPage = Math.max(1, Math.ceil(totalCount / PER_PAGE));

  function renderContent() {
    if (loading && clips.length === 0) {
      return <LoadingIndicator />;
    }

    if (error) {
      return <div className="text-danger">{error.message}</div>;
    }

    if (clips.length === 0) {
      return <div className="mt-4">No clips found.</div>;
    }

    return <ClipCardGrid clips={clips} />;
  }

  return (
    <div className="clip-list-container p-3">
      <div className="d-flex align-items-center justify-content-between mb-3">
        <h4 className="mb-0">Clips ({totalCount})</h4>
        <div className="d-flex" style={{ gap: "0.5rem" }}>
          {totalCount > 0 && (
            <Link to="/clips/feed" className="btn btn-secondary">
              Feed
            </Link>
          )}
          <Button variant="primary" onClick={() => setShowCreate(true)}>
            Create Clip
          </Button>
        </div>
      </div>

      {renderContent()}

      {totalCount > PER_PAGE && (
        <div className="d-flex align-items-center justify-content-center gap-2 mt-3">
          <Button
            variant="secondary"
            disabled={page <= 1}
            onClick={() => setPage((p) => Math.max(1, p - 1))}
          >
            Previous
          </Button>
          <span className="mx-2">
            Page {page} of {maxPage}
          </span>
          <Button
            variant="secondary"
            disabled={page >= maxPage}
            onClick={() => setPage((p) => p + 1)}
          >
            Next
          </Button>
        </div>
      )}

      {showCreate && (
        <ClipCreateModal
          onClose={(created) => {
            setShowCreate(false);
            if (created) {
              refetch();
            }
          }}
        />
      )}
    </div>
  );
};

export default ClipList;
