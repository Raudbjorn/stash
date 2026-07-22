import { useCallback } from "react";
import { useIntl } from "react-intl";
import * as GQL from "src/core/generated-graphql";
import { useToast } from "./Toast";

// Hook for getting all playlists
export const usePlaylists = () => {
  const { data, loading, error, refetch } = GQL.useAllPlaylistsQuery();

  return {
    playlists: data?.allPlaylists ?? [],
    loading,
    error,
    refetch,
  };
};

// Hook for getting the default playlist
export const useDefaultPlaylist = () => {
  const { data, loading, error, refetch } = GQL.useDefaultPlaylistQuery();

  return {
    defaultPlaylist: data?.defaultPlaylist,
    loading,
    error,
    refetch,
  };
};

// Hook for getting playlist entries
export const usePlaylistEntries = (playlistId: string) => {
  const { data, loading, error, refetch } = GQL.usePlaylistEntriesQuery({
    variables: { playlist_id: playlistId },
    skip: !playlistId,
  });

  return {
    entries: data?.playlistEntries ?? [],
    loading,
    error,
    refetch,
  };
};

// Hook for getting playlists containing a scene
export const usePlaylistsWithScene = (sceneId: string) => {
  const { data, loading, error, refetch } = GQL.usePlaylistsWithSceneQuery({
    variables: { scene_id: sceneId },
    skip: !sceneId,
  });

  return {
    playlists: data?.playlistsWithScene ?? [],
    loading,
    error,
    refetch,
  };
};

// Hook for playlist mutations
export const usePlaylistMutations = () => {
  const Toast = useToast();
  const intl = useIntl();

  const [createMutation] = GQL.usePlaylistCreateMutation();
  const [updateMutation] = GQL.usePlaylistUpdateMutation();
  const [destroyMutation] = GQL.usePlaylistDestroyMutation();
  const [addScenesMutation] = GQL.usePlaylistAddScenesMutation();
  const [removeSceneMutation] = GQL.usePlaylistRemoveSceneMutation();
  const [reorderSceneMutation] = GQL.usePlaylistReorderSceneMutation();
  const [clearScenesMutation] = GQL.usePlaylistClearScenesMutation();
  const [setDefaultMutation] = GQL.usePlaylistSetDefaultMutation();

  const createPlaylist = useCallback(
    async (name: string, description?: string) => {
      try {
        const result = await createMutation({
          variables: { input: { name, description } },
          refetchQueries: [GQL.AllPlaylistsDocument],
        });
        Toast.success(
          intl.formatMessage(
            { id: "toast.created_entity" },
            { entity: intl.formatMessage({ id: "playlist" }).toLocaleLowerCase() }
          )
        );
        return result.data?.playlistCreate;
      } catch (error) {
        Toast.error(error);
        throw error;
      }
    },
    [createMutation, Toast, intl]
  );

  const updatePlaylist = useCallback(
    async (id: string, name?: string, description?: string) => {
      try {
        const result = await updateMutation({
          variables: { input: { id, name, description } },
          refetchQueries: [GQL.AllPlaylistsDocument],
        });
        Toast.success(
          intl.formatMessage(
            { id: "toast.updated_entity" },
            { entity: intl.formatMessage({ id: "playlist" }).toLocaleLowerCase() }
          )
        );
        return result.data?.playlistUpdate;
      } catch (error) {
        Toast.error(error);
        throw error;
      }
    },
    [updateMutation, Toast, intl]
  );

  const deletePlaylist = useCallback(
    async (id: string) => {
      try {
        await destroyMutation({
          variables: { id },
          refetchQueries: [GQL.AllPlaylistsDocument],
        });
        Toast.success(
          intl.formatMessage(
            { id: "toast.delete_past_tense" },
            {
              count: 1,
              singularEntity: intl.formatMessage({ id: "playlist" }),
              pluralEntity: intl.formatMessage({ id: "playlists" }),
            }
          )
        );
      } catch (error) {
        Toast.error(error);
        throw error;
      }
    },
    [destroyMutation, Toast, intl]
  );

  const addScenesToPlaylist = useCallback(
    async (playlistId: string, sceneIds: string[]) => {
      try {
        await addScenesMutation({
          variables: { input: { playlist_id: playlistId, scene_ids: sceneIds } },
        });
        Toast.success(
          intl.formatMessage(
            { id: "toast.added_entity" },
            {
              count: sceneIds.length,
              singularEntity: intl.formatMessage({ id: "scene" }),
              pluralEntity: intl.formatMessage({ id: "scenes" }),
            }
          )
        );
      } catch (error) {
        Toast.error(error);
        throw error;
      }
    },
    [addScenesMutation, Toast, intl]
  );

  const removeSceneFromPlaylist = useCallback(
    async (playlistId: string, sceneId: string) => {
      try {
        await removeSceneMutation({
          variables: { input: { playlist_id: playlistId, scene_id: sceneId } },
        });
      } catch (error) {
        Toast.error(error);
        throw error;
      }
    },
    [removeSceneMutation, Toast]
  );

  const reorderSceneInPlaylist = useCallback(
    async (playlistId: string, sceneId: string, position: number) => {
      try {
        await reorderSceneMutation({
          variables: {
            input: { playlist_id: playlistId, scene_id: sceneId, position },
          },
        });
      } catch (error) {
        Toast.error(error);
        throw error;
      }
    },
    [reorderSceneMutation, Toast]
  );

  const clearPlaylist = useCallback(
    async (playlistId: string) => {
      try {
        await clearScenesMutation({
          variables: { playlist_id: playlistId },
        });
        Toast.success(intl.formatMessage({ id: "toast.playlist_cleared" }));
      } catch (error) {
        Toast.error(error);
        throw error;
      }
    },
    [clearScenesMutation, Toast, intl]
  );

  const setDefaultPlaylist = useCallback(
    async (id: string) => {
      try {
        await setDefaultMutation({
          variables: { id },
          refetchQueries: [GQL.AllPlaylistsDocument, GQL.DefaultPlaylistDocument],
        });
        Toast.success(intl.formatMessage({ id: "toast.default_playlist_set" }));
      } catch (error) {
        Toast.error(error);
        throw error;
      }
    },
    [setDefaultMutation, Toast, intl]
  );

  return {
    createPlaylist,
    updatePlaylist,
    deletePlaylist,
    addScenesToPlaylist,
    removeSceneFromPlaylist,
    reorderSceneInPlaylist,
    clearPlaylist,
    setDefaultPlaylist,
  };
};
