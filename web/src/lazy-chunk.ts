// A deploy replaces the hashed chunks, so a tab opened before it fails to
// load a page it has not visited yet: the old chunk is gone from the
// server. Reloading fetches the new index.html and its chunks. The marker
// keeps a chunk that is really missing from reloading the page in a loop.
const reloadKey = "mosdns.chunk-reload";
const reloadWindowMS = 30_000;

// reloadForNewBuild reloads the page unless it already did so recently,
// and reports whether it did.
export function reloadForNewBuild(reload = () => window.location.reload()) {
  try {
    const last = Number(sessionStorage.getItem(reloadKey) ?? 0);
    if (Date.now() - last < reloadWindowMS) return false;
    sessionStorage.setItem(reloadKey, String(Date.now()));
  } catch {
    return false;
  }
  reload();
  return true;
}

// loadChunk runs the import of a code-split module and reloads the page
// once when the chunk cannot be fetched, instead of failing the route.
// Wrap it in React.lazy: lazy(() => loadChunk(() => import("./page"))).
export function loadChunk<M>(load: () => Promise<M>): Promise<M> {
  return load().catch((error: unknown) => {
    // The page is going away; never settle so nothing renders an error.
    if (reloadForNewBuild()) return new Promise<M>(() => {});
    throw error;
  });
}
