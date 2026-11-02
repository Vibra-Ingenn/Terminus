(function () {
  "use strict";

  const rows = [];
  let selectedIndex = -1;

  const dropZone = document.getElementById("drop_zone");
  const fileInput = document.getElementById("file_input");
  const folderInput = document.getElementById("folder_input");
  const resultsBody = document.getElementById("results_body");
  const statusLine = document.getElementById("status_line");
  const medicalPack = document.getElementById("medical_pack");
  const detailPanel = document.getElementById("detail_panel");

  const billFields = ["vendor", "customer", "invoice_number", "date", "due_date", "total"];

  function setStatus(msg) {
    statusLine.textContent = msg || "";
  }

  function escapeHtml(s) {
    return String(s)
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;")
      .replace(/"/g, "&quot;");
  }

  function cell(value, empty) {
    const v = value == null ? "" : String(value);
    const isEmpty = empty || v.trim() === "";
    const title = isEmpty ? "Not found in this document" : v;
    const cls = isEmpty ? "empty" : "";
    const text = isEmpty ? "—" : v;
    return `<td class="${cls}" title="${escapeHtml(title)}">${escapeHtml(text)}</td>`;
  }

  function duplicateKey(row) {
    const f = row.fields || {};
    const v = (f.vendor || "").trim().toLowerCase();
    const inv = (f.invoice_number || "").trim().toLowerCase();
    const t = (f.total || "").trim().toLowerCase();
    if (!v && !inv && !t) {
      return "";
    }
    return v + "|" + inv + "|" + t;
  }

  function markDuplicates() {
    const seen = new Map();
    rows.forEach((row, idx) => {
      row.duplicate = false;
      if (row.not_bill) {
        return;
      }
      const key = duplicateKey(row);
      if (!key) {
        return;
      }
      if (seen.has(key)) {
        row.duplicate = true;
        rows[seen.get(key)].duplicate = true;
      } else {
        seen.set(key, idx);
      }
    });
  }

  function isNotBill(fields) {
    if (!fields) {
      return true;
    }
    const t = (fields.document_type || "").trim();
    if (t && t !== "invoice" && t !== "receipt") {
      return true;
    }
    return billFields.every((k) => !fields[k] || String(fields[k]).trim() === "");
  }

  function typeLabel(fields) {
    const t = (fields && fields.document_type) || "document";
    const map = {
      invoice: "Invoice",
      receipt: "Receipt",
      bank_statement: "Bank statement",
      letter: "Letter",
      medical_record: "Medical record",
      document: "Document",
    };
    return map[t] || "Document";
  }

  function normalizeDate(raw) {
    if (!raw || !String(raw).trim()) {
      return { norm: "", raw: "" };
    }
    const original = String(raw).trim();
    if (/^\d{4}-\d{2}-\d{2}$/.test(original)) {
      return { norm: original, raw: original };
    }
    const slash = original.match(/^(\d{1,2})\/(\d{1,2})\/(\d{2,4})$/);
    if (slash) {
      let y = parseInt(slash[3], 10);
      if (y < 100) {
        y += 2000;
      }
      const m = slash[1].padStart(2, "0");
      const d = slash[2].padStart(2, "0");
      return { norm: `${y}-${m}-${d}`, raw: original };
    }
    const words = original.match(/^([A-Za-z]+)\s+(\d{1,2}),\s+(\d{4})$/);
    if (words) {
      const months = {
        january: "01", february: "02", march: "03", april: "04", may: "05", june: "06",
        july: "07", august: "08", september: "09", october: "10", november: "11", december: "12",
      };
      const mo = months[words[1].toLowerCase()];
      if (mo) {
        const d = words[2].padStart(2, "0");
        return { norm: `${words[3]}-${mo}-${d}`, raw: original };
      }
    }
    return { norm: original, raw: original };
  }

  function renderTable() {
    markDuplicates();
    resultsBody.innerHTML = "";
    rows.forEach((row, idx) => {
      const f = row.fields || {};
      const tr = document.createElement("tr");
      if (row.not_bill) {
        tr.classList.add("not-bill");
      }
      if (row.duplicate) {
        tr.classList.add("duplicate");
      }
      if (idx === selectedIndex) {
        tr.classList.add("selected");
      }
      const notes = [];
      if (row.not_bill) {
        const tl = typeLabel(f);
        notes.push('<span class="tag grey" title="Document type from the text on the page">' + escapeHtml(tl) + "</span>");
      }
      if (row.duplicate) {
        notes.push('<span class="tag" title="Same vendor, invoice number and total as another row">Duplicate</span>');
      }
      if (row.error) {
        notes.push('<span class="tag" title="Could not read this file">' + escapeHtml(row.error) + "</span>");
      }
      const dateInfo = normalizeDate(f.date);
      const dueInfo = normalizeDate(f.due_date);
      tr.innerHTML =
        cell(row.file_name, false) +
        cell(typeLabel(f), false) +
        cell(f.vendor, !f.vendor) +
        cell(f.customer, !f.customer) +
        cell(f.invoice_number, !f.invoice_number) +
        cell(dateInfo.norm || dateInfo.raw, !f.date) +
        cell(dueInfo.norm || dueInfo.raw, !f.due_date) +
        cell(f.total, !f.total) +
        `<td>${notes.join(" ")}</td>`;
      tr.addEventListener("click", () => selectRow(idx));
      resultsBody.appendChild(tr);
    });
  }

  function selectRow(idx) {
    selectedIndex = idx;
    renderTable();
    const row = rows[idx];
    if (!row) {
      detailPanel.hidden = true;
      return;
    }
    detailPanel.hidden = false;
    document.getElementById("detail_title").textContent = row.file_name;
    const preview = document.getElementById("preview_box");
    preview.innerHTML = "";
    if (row.preview_url && row.preview_kind === "image") {
      const img = document.createElement("img");
      img.src = row.preview_url;
      img.alt = "Document image";
      preview.appendChild(img);
    } else if (row.raw_text) {
      preview.textContent = row.raw_text;
    } else if (row.error) {
      preview.textContent = row.error;
    } else {
      preview.textContent = "No preview available.";
    }

    const dl = document.getElementById("fields_list");
    dl.innerHTML = "";
    const fields = row.fields || {};
    const keys = Object.keys(fields).sort();
    if (keys.length === 0) {
      const dt = document.createElement("dt");
      dt.textContent = "No fields";
      const dd = document.createElement("dd");
      dd.textContent = "Nothing was found in this document.";
      dl.appendChild(dt);
      dl.appendChild(dd);
    } else {
      keys.forEach((k) => {
        const dt = document.createElement("dt");
        dt.textContent = k.replace(/_/g, " ");
        const dd = document.createElement("dd");
        dd.textContent = fields[k];
        dl.appendChild(dt);
        dl.appendChild(dd);
      });
    }
  }

  async function processFile(file) {
    const form = new FormData();
    form.append("file", file, file.name);
    form.append("include_raw", "true");
    let url = "/v1/parse";
    if (medicalPack.checked) {
      url += "?pack=medical";
    }
    const res = await fetch(url, { method: "POST", body: form });
    const data = await res.json().catch(() => ({}));
    if (!res.ok) {
      return {
        file_name: file.name,
        error: data.error || "Could not read this file",
        fields: {},
        not_bill: true,
      };
    }
    const fields = data.fields || {};
    const row = {
      file_name: file.name,
      fields: fields,
      raw_text: data.raw_text || "",
      stored_path: data.path || "",
      preview_kind: data.preview_kind || "text",
      preview_url: data.preview_url || "",
      not_bill: isNotBill(fields),
      error: data.status === "ok" ? "" : (data.warnings && data.warnings[0]) || "",
    };
    return row;
  }

  async function handleFiles(fileList) {
    const files = Array.from(fileList || []).filter((f) => f && f.name && !f.name.startsWith("."));
    if (files.length === 0) {
      return;
    }
    setStatus("Reading " + files.length + " file(s)…");
    for (const file of files) {
      try {
        const row = await processFile(file);
        rows.push(row);
      } catch (e) {
        rows.push({
          file_name: file.name,
          error: "Could not read this file",
          fields: {},
          not_bill: true,
        });
      }
    }
    renderTable();
    setStatus("Done. " + rows.length + " file(s) in the table.");
  }

  function exportRows() {
    const out = [];
    rows.forEach((row) => {
      const f = row.fields || {};
      const dateInfo = normalizeDate(f.date);
      const dueInfo = normalizeDate(f.due_date);
      out.push({
        file: row.file_name,
        type: typeLabel(f),
        vendor: f.vendor || "",
        customer: f.customer || "",
        invoice_number: f.invoice_number || "",
        date: dateInfo.norm,
        date_on_document: dateInfo.raw,
        due: dueInfo.norm,
        due_on_document: dueInfo.raw,
        total: f.total || "",
        notes: row.duplicate ? "Duplicate" : "",
      });
    });
    return out;
  }

  function downloadBlob(blob, name) {
    const a = document.createElement("a");
    a.href = URL.createObjectURL(blob);
    a.download = name;
    a.click();
    URL.revokeObjectURL(a.href);
  }

  function toCsv(data) {
    const headers = [
      "file", "type", "vendor", "customer", "invoice_number",
      "date", "date_on_document", "due", "due_on_document", "total", "notes",
    ];
    const lines = [headers.join(",")];
    data.forEach((row) => {
      const line = headers.map((h) => {
        const v = row[h] == null ? "" : String(row[h]);
        if (v.includes(",") || v.includes('"') || v.includes("\n")) {
          return '"' + v.replace(/"/g, '""') + '"';
        }
        return v;
      });
      lines.push(line.join(","));
    });
    return lines.join("\r\n");
  }

  function toExcelXml(data) {
    const headers = [
      "file", "type", "vendor", "customer", "invoice_number",
      "date", "date_on_document", "due", "due_on_document", "total", "notes",
    ];
    let rowsXml = "";
    data.forEach((row) => {
      rowsXml += "<Row>";
      headers.forEach((h) => {
        const v = escapeXml(row[h] || "");
        rowsXml += '<Cell><Data ss:Type="String">' + v + "</Data></Cell>";
      });
      rowsXml += "</Row>";
    });
    let headerXml = "<Row>";
    headers.forEach((h) => {
      headerXml += '<Cell><Data ss:Type="String">' + escapeXml(h) + "</Data></Cell>";
    });
    headerXml += "</Row>";
    return (
      '<?xml version="1.0"?><?mso-application progid="Excel.Sheet"?>' +
      '<Workbook xmlns="urn:schemas-microsoft-com:office:spreadsheet" ' +
      'xmlns:ss="urn:schemas-microsoft-com:office:spreadsheet">' +
      "<Worksheet ss:Name=\"Terminus\"><Table>" +
      headerXml +
      rowsXml +
      "</Table></Worksheet></Workbook>"
    );
  }

  function escapeXml(s) {
    return String(s)
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;")
      .replace(/"/g, "&quot;");
  }

  dropZone.addEventListener("dragover", (e) => {
    e.preventDefault();
    dropZone.classList.add("dragover");
  });
  dropZone.addEventListener("dragleave", () => dropZone.classList.remove("dragover"));
  dropZone.addEventListener("drop", async (e) => {
    e.preventDefault();
    dropZone.classList.remove("dragover");
    const items = e.dataTransfer.items;
    if (items && items.length) {
      const files = await collectFromDataTransfer(items);
      await handleFiles(files);
      return;
    }
    await handleFiles(e.dataTransfer.files);
  });

  async function collectFromDataTransfer(items) {
    const files = [];
    const entries = [];
    for (const item of items) {
      if (item.kind === "file") {
        const entry = item.webkitGetAsEntry && item.webkitGetAsEntry();
        if (entry) {
          entries.push(entry);
        } else {
          const f = item.getAsFile();
          if (f) {
            files.push(f);
          }
        }
      }
    }
    for (const entry of entries) {
      await walkEntry(entry, files);
    }
    return files;
  }

  function walkEntry(entry, files) {
    return new Promise((resolve) => {
      if (entry.isFile) {
        entry.file((f) => {
          files.push(f);
          resolve();
        }, () => resolve());
        return;
      }
      if (entry.isDirectory) {
        const reader = entry.createReader();
        reader.readEntries(async (ents) => {
          for (const ent of ents) {
            await walkEntry(ent, files);
          }
          resolve();
        }, () => resolve());
        return;
      }
      resolve();
    });
  }

  document.getElementById("btn_files").addEventListener("click", () => fileInput.click());
  document.getElementById("btn_folder").addEventListener("click", () => folderInput.click());
  fileInput.addEventListener("change", () => handleFiles(fileInput.files));
  folderInput.addEventListener("change", () => handleFiles(folderInput.files));
  document.getElementById("btn_close_detail").addEventListener("click", () => {
    selectedIndex = -1;
    detailPanel.hidden = true;
    renderTable();
  });
  document.getElementById("btn_csv").addEventListener("click", () => {
    const data = exportRows();
    if (!data.length) {
      setStatus("Add some files first.");
      return;
    }
    downloadBlob(new Blob([toCsv(data)], { type: "text/csv;charset=utf-8" }), "terminus-export.csv");
  });
  document.getElementById("btn_excel").addEventListener("click", () => {
    const data = exportRows();
    if (!data.length) {
      setStatus("Add some files first.");
      return;
    }
    downloadBlob(
      new Blob([toExcelXml(data)], { type: "application/vnd.ms-excel" }),
      "terminus-export.xls"
    );
  });
})();
