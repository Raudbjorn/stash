import React, { useState } from "react";
import { useIntl } from "react-intl";
import * as GQL from "src/core/generated-graphql";
import {
  ScrapedInputGroupRow,
  ScrapedStringListRow,
  ScrapedTextAreaRow,
} from "src/components/Shared/ScrapeDialog/ScrapeDialogRow";
import { ScrapeDialog } from "src/components/Shared/ScrapeDialog/ScrapeDialog";
import {
  ObjectScrapeResult,
  ScrapeResult,
} from "src/components/Shared/ScrapeDialog/scrapeResult";
import {
  ScrapedPerformersRow,
  ScrapedStudioRow,
} from "src/components/Shared/ScrapeDialog/ScrapedObjectsRow";
import {
  useMergeModeObjectList,
  useMergeModeStringList,
} from "src/components/Shared/ScrapeDialog/mergeMode";
import { Performer } from "src/components/Performers/PerformerSelect";
import {
  useCreateScrapedPerformer,
  useCreateScrapedStudio,
} from "src/components/Shared/ScrapeDialog/createObjects";
import { Tag } from "src/components/Tags/TagSelect";
import { Studio } from "src/components/Studios/StudioSelect";
import { useScrapedTags } from "src/components/Shared/ScrapeDialog/scrapedTags";

interface IGalleryScrapeDialogProps {
  gallery: Partial<GQL.GalleryUpdateInput>;
  galleryStudio: Studio | null;
  galleryTags: Tag[];
  galleryPerformers: Performer[];
  scraped: GQL.ScrapedGallery;

  onClose: (scrapedGallery?: GQL.ScrapedGallery) => void;
}

export const GalleryScrapeDialog: React.FC<IGalleryScrapeDialogProps> = ({
  gallery,
  galleryStudio,
  galleryTags,
  galleryPerformers,
  scraped,
  onClose,
}) => {
  const intl = useIntl();
  const [title, setTitle] = useState<ScrapeResult<string>>(
    new ScrapeResult<string>(gallery.title, scraped.title)
  );
  const [code, setCode] = useState<ScrapeResult<string>>(
    new ScrapeResult<string>(gallery.code, scraped.code)
  );
  const {
    result: urls,
    setResult: setURLs,
    mergeMode: urlsMergeMode,
    onSetMergeMode: onSetURLsMergeMode,
  } = useMergeModeStringList("gallery.urls", gallery.urls, scraped.urls);
  const [date, setDate] = useState<ScrapeResult<string>>(
    new ScrapeResult<string>(gallery.date, scraped.date)
  );
  const [photographer, setPhotographer] = useState<ScrapeResult<string>>(
    new ScrapeResult<string>(gallery.photographer, scraped.photographer)
  );
  const [studio, setStudio] = useState<ObjectScrapeResult<GQL.ScrapedStudio>>(
    new ObjectScrapeResult<GQL.ScrapedStudio>(
      galleryStudio
        ? {
            stored_id: galleryStudio.id,
            name: galleryStudio.name,
          }
        : undefined,
      scraped.studio
    )
  );
  const [newStudio, setNewStudio] = useState<GQL.ScrapedStudio | undefined>(
    scraped.studio && !scraped.studio.stored_id ? scraped.studio : undefined
  );

  const {
    result: performers,
    setResult: setPerformers,
    mergeMode: performersMergeMode,
    onSetMergeMode: onSetPerformersMergeMode,
  } = useMergeModeObjectList<GQL.ScrapedPerformer>(
    "gallery.performers",
    galleryPerformers.map((p) => ({
      stored_id: p.id,
      name: p.name,
    })),
    scraped.performers
  );
  const [newPerformers, setNewPerformers] = useState<GQL.ScrapedPerformer[]>(
    scraped.performers?.filter((t) => !t.stored_id) ?? []
  );

  const { tags, newTags, scrapedTagsRow, linkDialog } = useScrapedTags(
    galleryTags,
    scraped.tags,
    { mergeModeField: "gallery.tags" }
  );

  const [details, setDetails] = useState<ScrapeResult<string>>(
    new ScrapeResult<string>(gallery.details, scraped.details)
  );

  const createNewStudio = useCreateScrapedStudio({
    scrapeResult: studio,
    setScrapeResult: setStudio,
    setNewObject: setNewStudio,
  });

  const createNewPerformer = useCreateScrapedPerformer({
    scrapeResult: performers,
    setScrapeResult: setPerformers,
    newObjects: newPerformers,
    setNewObjects: setNewPerformers,
  });

  // don't show the dialog if nothing was scraped
  if (
    [
      title,
      code,
      urls,
      date,
      photographer,
      studio,
      performers,
      tags,
      details,
    ].every((r) => !r.scraped) &&
    !newStudio &&
    newPerformers.length === 0 &&
    newTags.length === 0
  ) {
    onClose();
    return null;
  }

  function makeNewScrapedItem(): GQL.ScrapedGalleryDataFragment {
    const newStudioValue = studio.getNewValue();

    return {
      title: title.getNewValue(),
      code: code.getNewValue(),
      urls: urls.getNewValue(),
      date: date.getNewValue(),
      photographer: photographer.getNewValue(),
      studio: newStudioValue,
      performers: performers.getNewValue(),
      tags: tags.getNewValue(),
      details: details.getNewValue(),
    };
  }

  function renderScrapeRows() {
    return (
      <>
        <ScrapedInputGroupRow
          field="title"
          title={intl.formatMessage({ id: "title" })}
          result={title}
          onChange={(value) => setTitle(value)}
        />
        <ScrapedInputGroupRow
          field="code"
          title={intl.formatMessage({ id: "scene_code" })}
          result={code}
          onChange={(value) => setCode(value)}
        />
        <ScrapedStringListRow
          field="urls"
          title={intl.formatMessage({ id: "urls" })}
          result={urls}
          onChange={(value) => setURLs(value)}
          mergeMode={urlsMergeMode}
          onSetMergeMode={onSetURLsMergeMode}
        />
        <ScrapedInputGroupRow
          field="date"
          title={intl.formatMessage({ id: "date" })}
          placeholder="YYYY-MM-DD"
          result={date}
          onChange={(value) => setDate(value)}
        />
        <ScrapedInputGroupRow
          field="photographer"
          title={intl.formatMessage({ id: "photographer" })}
          result={photographer}
          onChange={(value) => setPhotographer(value)}
        />
        <ScrapedStudioRow
          field="studio"
          title={intl.formatMessage({ id: "studios" })}
          result={studio}
          onChange={(value) => setStudio(value)}
          newStudio={newStudio}
          onCreateNew={createNewStudio}
        />
        <ScrapedPerformersRow
          field="performers"
          title={intl.formatMessage({ id: "performers" })}
          result={performers}
          onChange={(value) => setPerformers(value)}
          newObjects={newPerformers}
          onCreateNew={createNewPerformer}
          ageFromDate={date.useNewValue ? date.newValue : date.originalValue}
          mergeMode={performersMergeMode}
          onSetMergeMode={onSetPerformersMergeMode}
        />
        {scrapedTagsRow}
        <ScrapedTextAreaRow
          field="details"
          title={intl.formatMessage({ id: "details" })}
          result={details}
          onChange={(value) => setDetails(value)}
        />
      </>
    );
  }

  if (linkDialog) {
    return linkDialog;
  }

  return (
    <ScrapeDialog
      title={intl.formatMessage(
        { id: "dialogs.scrape_entity_title" },
        { entity_type: intl.formatMessage({ id: "gallery" }) }
      )}
      onClose={(apply) => {
        onClose(apply ? makeNewScrapedItem() : undefined);
      }}
    >
      {renderScrapeRows()}
    </ScrapeDialog>
  );
};
