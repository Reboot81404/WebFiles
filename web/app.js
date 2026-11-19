(() => {
  "use strict";

  const $ = (id) => document.getElementById(id);
  const state = {
    path: "/",
    entries: [],
    selected: new Set(),
    opened: null,
    dirty: false,
    clipboard: null,
    lastSelected: null,
    draggedNames: [],
    contextName: null,
    toastTimer: null,
  };

  const imageExt = new Set(["jpg", "jpeg", "png", "gif", "webp", "avif", "bmp"]);
  const videoExt = new Set(["mp4", "webm", "ogv"]);
  const audioExt = new Set(["mp3", "wav", "ogg", "m4a"]);

  async function request(url, options = {}) {
    const response = await fetch(url, options);
    if (!response.ok) {
      let message = `${response.status} ${response.statusText}`;
      try {
        const data = await response.json();
        if (data.error) message = data.error;
      } catch (_) { /* Response was not JSON. */ }
      throw new Error(message);
    }
    if (response.status === 204) return null;
    const type = response.headers.get("content-type") || "";
    return type.includes("application/json") ? response.json() : response;
  }

  function mutateOptions(method, body) {
    return {
      method,
      headers: {
        "Content-Type": "application/json",
        "X-WebEdit-Request": "1",
      },
      body: JSON.stringify(body),
    };
  }

  function showToast(message) {
    const toast = $("toast");
    toast.textContent = message;
    toast.classList.add("show");
    clearTimeout(state.toastTimer);
    state.toastTimer = setTimeout(() => toast.classList.remove("show"), 2800);
  }

  function setStatus(message) {
    $("status-message").textContent = message;
  }

  function pathJoin(parent, name) {
    return parent === "/" ? `/${name}` : `${parent}/${name}`;
  }

  function parentPath(value) {
    if (value === "/") return "/";
    return value.slice(0, value.lastIndexOf("/")) || "/";
  }

  function formatBytes(bytes) {
    if (bytes < 1024) return `${bytes} B`;
    const units = ["KB", "MB", "GB", "TB"];
    let value = bytes / 1024;
    let index = 0;
    while (value >= 1024 && index < units.length - 1) {
      value /= 1024;
      index += 1;
    }
    return `${value.toFixed(value >= 10 ? 0 : 1)} ${units[index]}`;
  }

  function extension(name) {
    const index = name.lastIndexOf(".");
    return index < 0 ? "" : name.slice(index + 1).toLowerCase();
  }

  function openedFilePath() {
    return state.opened ? pathJoin(state.path, state.opened.name) : null;
  }

  function selectedEntries() {
    return state.entries.filter((entry) => state.selected.has(entry.name));
  }

  function confirmDiscard() {
    return !state.dirty || window.confirm("Kaydedilmemiş değişiklikler silinsin mi?");
  }

  function clearWorkspace(clearOpened = true) {
    if (clearOpened) state.opened = null;
    $("welcome").hidden = false;
    $("editor-wrap").hidden = true;
    $("preview-wrap").hidden = true;
    $("preview-wrap").replaceChildren();
    $("unsupported").hidden = true;
    $("document-title").textContent = "Dosya açık değil";
    $("download-btn").disabled = true;
    $("save-btn").disabled = true;
    $("file-meta").textContent = "Dosya yok";
    $("editor").value = "";
  }

  async function loadDirectory(nextPath = state.path) {
    if (!confirmDiscard()) return;
    try {
      setStatus("Yükleniyor…");
      const data = await request(`/api/list?path=${encodeURIComponent(nextPath)}`);
      state.path = data.path;
      state.entries = data.entries;
      state.selected.clear();
      state.lastSelected = null;
      state.dirty = false;
      clearWorkspace();
      renderBreadcrumb();
      renderEntries();
      setStatus("Hazır");
      $("file-meta").textContent = `${state.entries.length} öğe`;
      $("list-note").textContent = data.truncated ? `İlk ${state.entries.length} öğe gösteriliyor` : "";
    } catch (error) {
      showToast(error.message);
      setStatus("Klasör yüklenemedi");
    }
  }

  async function refreshListing() {
    try {
      const data = await request(`/api/list?path=${encodeURIComponent(state.path)}`);
      state.entries = data.entries;
      const names = new Set(state.entries.map((entry) => entry.name));
      state.selected = new Set([...state.selected].filter((name) => names.has(name)));
      if (state.opened) {
        const updated = state.entries.find((entry) => entry.name === state.opened.name);
        if (updated) state.opened = updated;
        else {
          state.dirty = false;
          clearWorkspace();
        }
      }
      renderEntries();
      if (state.opened) {
        const entry = state.opened;
        $("file-meta").textContent = `${formatBytes(entry.size)} · ${new Date(entry.modified).toLocaleString("tr-TR")}`;
      } else {
        $("file-meta").textContent = `${state.entries.length} öğe`;
      }
      $("list-note").textContent = data.truncated ? `İlk ${state.entries.length} öğe gösteriliyor` : "";
    } catch (error) {
      showToast(error.message);
    }
  }

  function renderBreadcrumb() {
    const container = $("breadcrumb");
    container.replaceChildren();
    const rootButton = document.createElement("button");
    rootButton.type = "button";
    rootButton.className = "crumb";
    rootButton.textContent = "/";
    rootButton.title = "Ana klasör";
    rootButton.addEventListener("click", () => loadDirectory("/"));
    container.append(rootButton);

    if (state.path === "/") return;
    const parts = state.path.split("/").filter(Boolean);
    let accumulated = "";
    for (const part of parts) {
      const separator = document.createElement("span");
      separator.className = "crumb-separator";
      separator.textContent = "/";
      container.append(separator);
      accumulated += `/${part}`;
      const target = accumulated;
      const button = document.createElement("button");
      button.type = "button";
      button.className = "crumb";
      button.textContent = part;
      button.addEventListener("click", () => loadDirectory(target));
      container.append(button);
    }
  }

  function renderEntries() {
    const list = $("file-list");
    const query = $("search-input").value.trim().toLocaleLowerCase("tr-TR");
    const visibleEntries = state.entries.filter((entry) => entry.name.toLocaleLowerCase("tr-TR").includes(query));
    list.replaceChildren();

    for (const entry of visibleEntries) {
      const row = document.createElement("button");
      row.type = "button";
      row.draggable = true;
      row.className = `file-row ${entry.type === "directory" ? "directory" : ""}`;
      if (state.selected.has(entry.name)) row.classList.add("selected");
      if (state.opened && state.opened.name === entry.name) row.classList.add("opened");

      const icon = document.createElement("span");
      icon.className = "file-icon";
      icon.textContent = entry.type === "directory" ? "▰" : entry.type === "symlink" ? "↗" : "▤";
      const name = document.createElement("span");
      name.className = "file-name";
      name.textContent = entry.name;
      const size = document.createElement("span");
      size.className = "file-size";
      size.textContent = entry.type === "directory" ? "" : formatBytes(entry.size);
      row.append(icon, name, size);

      row.addEventListener("click", (event) => selectEntry(entry, event));
      row.addEventListener("dblclick", () => openEntry(entry));
      row.addEventListener("contextmenu", (event) => {
        event.preventDefault();
        if (!state.selected.has(entry.name)) {
          state.selected.clear();
          state.selected.add(entry.name);
          state.lastSelected = entry.name;
          renderEntries();
        }
        showContextMenu(event.clientX, event.clientY, entry);
      });
      row.addEventListener("dragstart", (event) => {
        if (!state.selected.has(entry.name)) {
          state.selected.clear();
          state.selected.add(entry.name);
          updateActionButtons();
          row.classList.add("selected");
        }
        state.draggedNames = [...state.selected];
        event.dataTransfer.effectAllowed = "move";
        event.dataTransfer.setData("text/plain", "webedit-move");
        row.classList.add("dragging");
      });
      row.addEventListener("dragend", () => {
        row.classList.remove("dragging");
        state.draggedNames = [];
      });
      row.addEventListener("dragover", (event) => {
        const externalFiles = [...event.dataTransfer.types].includes("Files");
        if (entry.type === "directory" && (state.draggedNames.length || externalFiles)) {
          event.preventDefault();
          event.dataTransfer.dropEffect = externalFiles ? "copy" : "move";
          row.classList.add("drop-target");
        }
      });
      row.addEventListener("dragleave", () => row.classList.remove("drop-target"));
      row.addEventListener("drop", async (event) => {
        event.preventDefault();
        event.stopPropagation();
        row.classList.remove("drop-target");
        if (event.dataTransfer.files && event.dataTransfer.files.length && entry.type === "directory") {
          for (const file of event.dataTransfer.files) await uploadFile(file, pathJoin(state.path, entry.name));
        } else if (state.draggedNames.length && entry.type === "directory") {
          await moveNamesToDirectory(state.draggedNames, pathJoin(state.path, entry.name));
          state.draggedNames = [];
        }
      });
      list.append(row);
    }

    if (visibleEntries.length === 0) {
      const empty = document.createElement("div");
      empty.className = "empty-list";
      empty.textContent = query ? "Eşleşen öğe bulunamadı" : "Bu klasör boş";
      list.append(empty);
    }
    $("item-count").textContent = String(state.entries.length);
    $("selection-count").textContent = state.selected.size ? `${state.selected.size} seçili` : "";
    updateActionButtons();
  }

  function selectEntry(entry, event) {
    const visibleEntries = state.entries.filter((item) => item.name.toLocaleLowerCase("tr-TR").includes($("search-input").value.trim().toLocaleLowerCase("tr-TR")));
    if (event.shiftKey && state.lastSelected) {
      const start = visibleEntries.findIndex((item) => item.name === state.lastSelected);
      const end = visibleEntries.findIndex((item) => item.name === entry.name);
      if (start >= 0 && end >= 0) {
        if (!event.ctrlKey && !event.metaKey) state.selected.clear();
        for (const item of visibleEntries.slice(Math.min(start, end), Math.max(start, end) + 1)) {
          state.selected.add(item.name);
        }
      }
    } else if (event.ctrlKey || event.metaKey) {
      if (state.selected.has(entry.name)) state.selected.delete(entry.name);
      else state.selected.add(entry.name);
      state.lastSelected = entry.name;
    } else {
      state.selected.clear();
      state.selected.add(entry.name);
      state.lastSelected = entry.name;
    }
    renderEntries();
  }

  function updateActionButtons() {
    const selected = selectedEntries();
    const single = selected.length === 1 ? selected[0] : null;
    $("rename-btn").disabled = !single;
    $("delete-btn").disabled = selected.length === 0;
    $("copy-btn").disabled = selected.length === 0;
    $("cut-btn").disabled = selected.length === 0;
    $("paste-btn").disabled = !state.clipboard || state.clipboard.items.length === 0;
    $("download-btn").disabled = !single || single.type !== "file";
    const pasteLabel = state.clipboard ? `Yapıştır (${state.clipboard.items.length})` : "Yapıştır";
    $("paste-btn").textContent = pasteLabel;
  }

  async function openEntry(entry) {
    if (entry.type === "directory") {
      await loadDirectory(pathJoin(state.path, entry.name));
      return;
    }
    if (entry.type === "symlink" || entry.type === "other") {
      showToast("Sembolik bağlantılar ve özel dosyalar editörde açılamaz.");
      return;
    }
    if (state.opened && state.opened.name === entry.name) return;
    if (state.opened && !confirmDiscard()) return;
    state.selected.clear();
    state.selected.add(entry.name);
    state.lastSelected = entry.name;
    state.dirty = false;
    state.opened = entry;
    renderEntries();
    await openFile(entry);
  }

  async function openFile(entry) {
    const filePath = pathJoin(state.path, entry.name);
    const ext = extension(entry.name);
    clearWorkspace(false);
    $("document-title").textContent = entry.name;
    $("download-btn").disabled = entry.type !== "file";
    $("save-btn").disabled = true;
    $("file-meta").textContent = `${formatBytes(entry.size)} · ${new Date(entry.modified).toLocaleString("tr-TR")}`;
    $("welcome").hidden = true;

    if (imageExt.has(ext) || videoExt.has(ext) || audioExt.has(ext) || ext === "pdf") {
      showPreview(filePath, ext);
      setStatus("Önizleme");
      return;
    }
    try {
      const data = await request(`/api/read?path=${encodeURIComponent(filePath)}`);
      $("editor").value = data.content;
      $("editor-wrap").hidden = false;
      $("save-btn").disabled = false;
      setStatus("Metin dosyası");
    } catch (error) {
      $("unsupported").hidden = false;
      $("unsupported").querySelector("p").textContent = error.message;
      setStatus("Önizleme kullanılamıyor");
    }
  }

  function showPreview(filePath, ext) {
    const wrap = $("preview-wrap");
    wrap.replaceChildren();
    const src = `/api/preview?path=${encodeURIComponent(filePath)}`;
    let element;
    if (imageExt.has(ext)) {
      element = document.createElement("img");
      element.alt = filePath;
      element.src = src;
    } else if (videoExt.has(ext)) {
      element = document.createElement("video");
      element.controls = true;
      element.preload = "metadata";
      element.src = src;
    } else if (audioExt.has(ext)) {
      element = document.createElement("audio");
      element.controls = true;
      element.src = src;
    } else {
      element = document.createElement("iframe");
      element.title = "PDF önizleme";
      element.src = src;
    }
    wrap.append(element);
    wrap.hidden = false;
  }

  async function createItem(kind) {
    const label = kind === "file" ? "Dosya adı" : "Klasör adı";
    const name = window.prompt(label);
    if (name === null) return;
    if (!validName(name)) {
      showToast("Eğik çizgi içermeyen geçerli bir ad gir.");
      return;
    }
    try {
      await request(`/api/create-${kind === "file" ? "file" : "dir"}`, mutateOptions("POST", { path: state.path, name }));
      showToast(`${kind === "file" ? "Dosya" : "Klasör"} oluşturuldu`);
      await refreshListing();
    } catch (error) {
      showToast(error.message);
    }
  }

  function validName(name) {
    return !!name && name !== "." && name !== ".." && !/[\\/\0]/.test(name);
  }

  async function saveFile() {
    const filePath = openedFilePath();
    if (!filePath || $("editor-wrap").hidden || $("save-btn").disabled) return;
    const editor = $("editor");
    const saveButton = $("save-btn");
    editor.disabled = true;
    saveButton.disabled = true;
    try {
      setStatus("Kaydediliyor…");
      await request("/api/write", mutateOptions("PUT", { path: filePath, content: editor.value }));
      state.dirty = false;
      setStatus("Kaydedildi");
      showToast("Kaydedildi");
      await refreshListing();
    } catch (error) {
      setStatus("Kaydetme başarısız");
      showToast(error.message);
    } finally {
      editor.disabled = false;
      saveButton.disabled = false;
    }
  }

  async function deleteSelected() {
    const entries = selectedEntries();
    if (!entries.length) return;
    const openedWillBeDeleted = state.opened && entries.some((entry) => entry.name === state.opened.name);
    if (openedWillBeDeleted && !confirmDiscard()) return;
    const label = entries.length === 1 ? `“${entries[0].name}” silinsin mi?` : `${entries.length} öğe silinsin mi?`;
    if (!window.confirm(`${label} Bu işlem geri alınamaz.`)) return;

    const failures = [];
    for (const entry of entries) {
      try {
        await request("/api/delete", mutateOptions("POST", { path: pathJoin(state.path, entry.name) }));
      } catch (error) {
        failures.push(`${entry.name}: ${error.message}`);
      }
    }
    if (openedWillBeDeleted) {
      state.dirty = false;
      clearWorkspace();
    }
    state.selected.clear();
    await refreshListing();
    showToast(failures.length ? `Bazı öğeler silinemedi: ${failures.join("; ")}` : "Silindi");
  }

  async function renameSelected() {
    const entries = selectedEntries();
    if (entries.length !== 1) return;
    const entry = entries[0];
    const isOpened = state.opened && state.opened.name === entry.name;
    if (isOpened && !confirmDiscard()) return;
    const name = window.prompt("Yeni ad", entry.name);
    if (name === null || name === entry.name) return;
    if (!validName(name)) {
      showToast("Eğik çizgi içermeyen geçerli bir ad gir.");
      return;
    }
    try {
      await request("/api/rename", mutateOptions("POST", {
        oldPath: pathJoin(state.path, entry.name),
        newPath: pathJoin(state.path, name),
      }));
      if (isOpened) {
        state.dirty = false;
        state.opened = null;
        clearWorkspace();
      }
      state.selected = new Set([name]);
      state.lastSelected = name;
      await refreshListing();
      if (isOpened) {
        const renamed = state.entries.find((item) => item.name === name);
        if (renamed) {
          state.opened = renamed;
          await openFile(renamed);
          renderEntries();
        }
      }
      showToast("Yeniden adlandırıldı");
    } catch (error) {
      showToast(error.message);
    }
  }

  function downloadSelected() {
    const entries = selectedEntries();
    if (entries.length !== 1 || entries[0].type !== "file") return;
    const filePath = pathJoin(state.path, entries[0].name);
    window.location.href = `/api/download?path=${encodeURIComponent(filePath)}`;
  }

  function setClipboard(operation) {
    const entries = selectedEntries();
    if (!entries.length) return;
    state.clipboard = {
      operation,
      items: entries.map((entry) => ({ path: pathJoin(state.path, entry.name), name: entry.name })),
    };
    updateActionButtons();
    const verb = operation === "move" ? "Taşımak" : "Kopyalamak";
    showToast(`${entries.length} öğe ${verb.toLocaleLowerCase("tr-TR")} için işaretlendi. Hedef klasöre gidip Yapıştır'a bas.`);
  }

  async function pasteClipboard() {
    if (!state.clipboard || !state.clipboard.items.length) return;
    const clipboard = state.clipboard;
    const failures = [];
    const remaining = [];
    let completed = 0;
    for (const item of clipboard.items) {
      const destination = pathJoin(state.path, item.name);
      if (destination === item.path) {
        if (clipboard.operation === "move") remaining.push(item);
        continue;
      }
      try {
        if (clipboard.operation === "move") {
          await request("/api/rename", mutateOptions("POST", { oldPath: item.path, newPath: destination }));
        } else {
          await request("/api/copy", mutateOptions("POST", { source: item.path, destination }));
        }
        completed += 1;
      } catch (error) {
        failures.push(`${item.name}: ${error.message}`);
        if (clipboard.operation === "move") remaining.push(item);
      }
    }
    if (clipboard.operation === "move") {
      state.clipboard.items = remaining;
      if (remaining.length === 0) state.clipboard = null;
    }
    await refreshListing();
    updateActionButtons();
    if (failures.length) showToast(`${completed} öğe işlendi. ${failures.join("; ")}`);
    else showToast(completed ? `${completed} öğe ${clipboard.operation === "move" ? "taşındı" : "kopyalandı"}` : "İşlem yapılmadı");
  }

  async function moveNamesToDirectory(names, targetDirectory) {
    if (state.opened && names.includes(state.opened.name) && !confirmDiscard()) return;
    const failures = [];
    let moved = 0;
    for (const name of names) {
      const source = pathJoin(state.path, name);
      const destination = pathJoin(targetDirectory, name);
      if (source === destination || destination.startsWith(`${source}/`)) continue;
      try {
        await request("/api/rename", mutateOptions("POST", { oldPath: source, newPath: destination }));
        moved += 1;
      } catch (error) {
        failures.push(`${name}: ${error.message}`);
      }
    }
    state.selected.clear();
    await refreshListing();
    showToast(failures.length ? `${moved} taşındı. ${failures.join("; ")}` : `${moved} öğe taşındı`);
  }

  async function uploadFile(file, destinationPath = state.path) {
    const form = new FormData();
    form.append("file", file, file.name);
    try {
      setStatus("Yükleniyor…");
      await request(`/api/upload?path=${encodeURIComponent(destinationPath)}`, {
        method: "POST",
        headers: { "X-WebEdit-Request": "1" },
        body: form,
      });
      showToast(`${file.name} yüklendi`);
      if (destinationPath === state.path) await refreshListing();
      setStatus("Hazır");
    } catch (error) {
      setStatus("Yükleme başarısız");
      showToast(error.message);
    }
  }

  function showContextMenu(x, y, entry = null) {
    state.contextName = entry ? entry.name : null;
    const menu = $("context-menu");
    menu.hidden = false;
    menu.querySelectorAll("[data-requires-selection]").forEach((item) => {
      item.hidden = !entry;
    });
    menu.querySelector('[data-action="download"]').hidden = !entry || entry.type !== "file" || state.selected.size !== 1;
    menu.querySelector('[data-action="rename"]').disabled = state.selected.size !== 1;
    menu.querySelector('[data-action="copy"]').disabled = state.selected.size === 0;
    menu.querySelector('[data-action="cut"]').disabled = state.selected.size === 0;
    menu.querySelector('[data-action="delete"]').disabled = state.selected.size === 0;
    menu.querySelector('[data-action="paste"]').disabled = !state.clipboard;
    const width = 190;
    const height = menu.offsetHeight;
    menu.style.left = `${Math.max(8, Math.min(x, window.innerWidth - width - 8))}px`;
    menu.style.top = `${Math.max(8, Math.min(y, window.innerHeight - height - 8))}px`;
  }

  function hideContextMenu() {
    $("context-menu").hidden = true;
  }

  function runContextAction(action) {
    hideContextMenu();
    switch (action) {
      case "open": {
        const entry = state.entries.find((item) => item.name === state.contextName) || selectedEntries()[0];
        if (entry) openEntry(entry);
        break;
      }
      case "rename": renameSelected(); break;
      case "copy": setClipboard("copy"); break;
      case "cut": setClipboard("move"); break;
      case "paste": pasteClipboard(); break;
      case "delete": deleteSelected(); break;
      case "download": downloadSelected(); break;
    }
  }

  $("refresh-btn").addEventListener("click", () => refreshListing());
  $("up-btn").addEventListener("click", () => {
    if (state.path !== "/") loadDirectory(parentPath(state.path));
  });
  $("new-file-btn").addEventListener("click", () => createItem("file"));
  $("new-dir-btn").addEventListener("click", () => createItem("directory"));
  $("upload-btn").addEventListener("click", () => $("upload-input").click());
  $("upload-input").addEventListener("change", async (event) => {
    const files = [...(event.target.files || [])];
    for (const file of files) await uploadFile(file);
    event.target.value = "";
  });
  $("save-btn").addEventListener("click", saveFile);
  $("delete-btn").addEventListener("click", deleteSelected);
  $("rename-btn").addEventListener("click", renameSelected);
  $("download-btn").addEventListener("click", downloadSelected);
  $("copy-btn").addEventListener("click", () => setClipboard("copy"));
  $("cut-btn").addEventListener("click", () => setClipboard("move"));
  $("paste-btn").addEventListener("click", pasteClipboard);
  $("search-input").addEventListener("input", renderEntries);

  $("context-menu").addEventListener("click", (event) => {
    const button = event.target.closest("[data-action]");
    if (button && !button.disabled) runContextAction(button.dataset.action);
    event.stopPropagation();
  });
  document.addEventListener("click", (event) => {
    if (!event.target.closest("#context-menu")) hideContextMenu();
  });
  document.addEventListener("keydown", (event) => {
    const target = event.target;
    const editingText = target instanceof HTMLElement && (target.isContentEditable || ["INPUT", "TEXTAREA"].includes(target.tagName));
    if (event.key === "Escape") hideContextMenu();
    if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "s" && state.opened && !$("save-btn").disabled) {
      event.preventDefault();
      saveFile();
      return;
    }
    if (editingText) return;
    if (event.key === "F2") {
      event.preventDefault();
      renameSelected();
    } else if (event.key === "Delete") {
      event.preventDefault();
      deleteSelected();
    } else if (event.key === "Enter" && state.selected.size === 1 && (target === document.body || target.classList.contains("file-row"))) {
      event.preventDefault();
      const entry = selectedEntries()[0];
      if (entry) openEntry(entry);
    } else if (event.key === "Backspace" && state.path !== "/") {
      event.preventDefault();
      loadDirectory(parentPath(state.path));
    } else if (event.key === "F5") {
      event.preventDefault();
      refreshListing();
    } else if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "a") {
      event.preventDefault();
      state.selected = new Set(state.entries.map((entry) => entry.name));
      renderEntries();
    } else if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "c") {
      event.preventDefault();
      setClipboard("copy");
    } else if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "x") {
      event.preventDefault();
      setClipboard("move");
    } else if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "v") {
      event.preventDefault();
      pasteClipboard();
    }
  });

  $("file-list").addEventListener("contextmenu", (event) => {
    if (event.target === $("file-list") || event.target.classList.contains("empty-list")) {
      event.preventDefault();
      state.selected.clear();
      renderEntries();
      showContextMenu(event.clientX, event.clientY);
    }
  });
  $("file-list").addEventListener("dragover", (event) => {
    if ([...event.dataTransfer.types].includes("Files")) event.preventDefault();
  });
  $("file-list").addEventListener("drop", async (event) => {
    if (event.dataTransfer.files && event.dataTransfer.files.length) {
      event.preventDefault();
      for (const file of event.dataTransfer.files) await uploadFile(file);
    }
  });
  $("editor").addEventListener("input", () => {
    state.dirty = true;
    setStatus("Kaydedilmemiş değişiklikler");
  });
  $("editor").addEventListener("keydown", (event) => {
    if (event.key === "Tab") {
      event.preventDefault();
      const start = event.target.selectionStart;
      const end = event.target.selectionEnd;
      event.target.setRangeText("\t", start, end, "end");
      state.dirty = true;
      setStatus("Kaydedilmemiş değişiklikler");
    }
  });
  window.addEventListener("beforeunload", (event) => {
    if (state.dirty) {
      event.preventDefault();
      event.returnValue = "";
    }
  });

  renderBreadcrumb();
  updateActionButtons();
  loadDirectory("/");
})();
