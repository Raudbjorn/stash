import { useCallback, useMemo } from "react";
import { useIntl } from "react-intl";
import * as GQL from "src/core/generated-graphql";
import { useToast } from "./Toast";

export const usePersistentQueue = () => {
  const Toast = useToast();
  const intl = useIntl();

  const { data, loading, refetch } = GQL.useSceneQueueQuery();
  const [addMutation] = GQL.useSceneQueueAddMutation();
  const [removeMutation] = GQL.useSceneQueueRemoveMutation();
  const [reorderMutation] = GQL.useSceneQueueReorderMutation();
  const [clearMutation] = GQL.useSceneQueueClearMutation();

  const queue = useMemo(
    () => (data?.sceneQueue ?? []).map((entry) => entry.scene),
    [data]
  );

  const addToQueue = useCallback(
    async (sceneId: string) => {
      try {
        await addMutation({ variables: { scene_id: sceneId } });
        await refetch();
        Toast.success(
          intl.formatMessage(
            { id: "toast.added_entity" },
            {
              count: 1,
              singularEntity: intl.formatMessage({ id: "scene" }),
              pluralEntity: intl.formatMessage({ id: "scenes" }),
            }
          )
        );
      } catch (error) {
        Toast.error(error);
      }
    },
    [addMutation, refetch, Toast, intl]
  );

  const removeFromQueue = useCallback(
    async (sceneId: string) => {
      try {
        await removeMutation({ variables: { scene_id: sceneId } });
        await refetch();
      } catch (error) {
        Toast.error(error);
      }
    },
    [removeMutation, refetch, Toast]
  );

  const clearQueue = useCallback(async () => {
    try {
      await clearMutation();
      await refetch();
      Toast.success(intl.formatMessage({ id: "toast.queue_cleared" }));
    } catch (error) {
      Toast.error(error);
    }
  }, [clearMutation, refetch, Toast, intl]);

  const reorderQueue = useCallback(
    async (fromIndex: number, toIndex: number) => {
      const scene = queue[fromIndex];
      if (!scene) return;
      try {
        await reorderMutation({
          variables: { scene_id: scene.id, position: toIndex },
        });
        await refetch();
      } catch (error) {
        Toast.error(error);
      }
    },
    [queue, reorderMutation, refetch, Toast]
  );

  const isInQueue = useCallback(
    (sceneId: string) => queue.some((scene) => scene.id === sceneId),
    [queue]
  );

  return {
    queue,
    loading,
    addToQueue,
    removeFromQueue,
    clearQueue,
    reorderQueue,
    isInQueue,
  };
};
