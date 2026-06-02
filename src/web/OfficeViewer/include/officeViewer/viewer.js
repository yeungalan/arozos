/**
 * Office Viewer — modern vanilla JS viewer
 * DOCX: rendered client-side by docx-preview (useBase64URL for images)
 * XLSX / PPTX: converted server-side via /api/officepreview/convert
 */

(function () {
  'use strict';

  /* ------------------------------------------------------------------ */
  /*  State                                                               */
  /* ------------------------------------------------------------------ */
  let currentSlide = 0;
  let totalSlides  = 0;
  let slideData    = null; // PPTXResult
  let currentSheet = 0;
  let sheetData    = null; // XLSXResult

  /* ------------------------------------------------------------------ */
  /*  Entry point                                                         */
  /* ------------------------------------------------------------------ */
  function init() {
    const inputFiles = ao_module_loadInputFiles();
    if (!inputFiles || inputFiles.length === 0) {
      window.location.href = './index.html';
      return;
    }

    const file = inputFiles[0];
    const ext  = file.filepath.split('.').pop().toLowerCase();
    const name = file.filename || file.filepath.split('/').pop();

    ao_module_setWindowTitle('Office Viewer — ' + name);
    setToolbarInfo(name, ext, file.filepath);

    loadFile(file.filepath, ext);
  }

  /* ------------------------------------------------------------------ */
  /*  Toolbar                                                             */
  /* ------------------------------------------------------------------ */
  function setToolbarInfo(name, ext, vpath) {
    const icon = document.getElementById('ov-file-icon');
    const nameEl = document.getElementById('ov-filename');
    const dlBtn  = document.getElementById('ov-btn-download');

    if (icon) {
      icon.className = 'ov-toolbar-icon ' + ext;
      icon.textContent = iconGlyph(ext);
    }
    if (nameEl) nameEl.textContent = name;
    if (dlBtn) {
      dlBtn.addEventListener('click', function () {
        window.location.href = '/media?download=true&file=' + encodeURIComponent(vpath);
      });
    }
  }

  function iconGlyph(ext) {
    switch (ext) {
      case 'docx': return 'W';
      case 'xlsx': return 'X';
      case 'pptx': return 'P';
      default:     return '?';
    }
  }

  /* ------------------------------------------------------------------ */
  /*  File loading                                                        */
  /* ------------------------------------------------------------------ */
  function loadFile(vpath, ext) {
    if (ext === 'docx' || ext === 'docm') {
      loadDocx(vpath);
    } else {
      loadViaBackend(vpath, ext);
    }
  }

  /* ------------------------------------------------------------------ */
  /*  DOCX — client-side via docx-preview + useBase64URL                 */
  /* ------------------------------------------------------------------ */
  function loadDocx(vpath) {
    showLoading('Loading document…');

    // Fetch the raw DOCX bytes from the media endpoint
    fetch('/media?file=' + encodeURIComponent(vpath))
      .then(function (res) {
        if (!res.ok) throw new Error('HTTP ' + res.status + ' — ' + res.statusText);
        return res.arrayBuffer();
      })
      .then(function (buffer) {
        renderDocx(buffer);
      })
      .catch(function (err) {
        showError('Failed to load document: ' + err.message);
      });
  }

  function renderDocx(buffer) {
    if (typeof window.docx === 'undefined') {
      showError('docx-preview library not loaded.');
      return;
    }

    const content = document.getElementById('ov-content');
    // Render directly into the scroll container — no extra wrapper div.
    // docx-preview will inject .docx-wrapper as a direct child.
    content.innerHTML = '<div id="ov-docx-scroll"></div>';

    const container = document.getElementById('ov-docx-scroll');

    window.docx.renderAsync(buffer, container, null, {
      className:          'docx',
      inWrapper:          true,
      ignoreWidth:        false,
      ignoreHeight:       false,
      ignoreFonts:        false,
      breakPages:         true,
      useBase64URL:       true,   // embed images as data: URIs
      experimental:       true,
      trimXmlDeclaration: true,
      debug:              false,
    })
    .then(function () {
      // docx-preview creates Blobs without a MIME type; FileReader then
      // produces data:application/octet-stream;base64,… which browsers
      // refuse to render as images.  Fix every affected <img> by sniffing
      // the binary magic bytes and rewriting the src.
      fixDocxImageMimeTypes(container);
    })
    .catch(function (err) {
      showError('DOCX render error: ' + (err && err.message ? err.message : String(err)));
    });
  }

  /* ------------------------------------------------------------------ */
  /*  Image MIME-type fix                                                 */
  /* ------------------------------------------------------------------ */

  /**
   * Walks every <img> inside container and replaces any
   * data:application/octet-stream src with the correct MIME type,
   * detected from the image's own magic bytes.
   */
  function fixDocxImageMimeTypes(container) {
    var imgs = container.querySelectorAll('img');
    for (var i = 0; i < imgs.length; i++) {
      var src = imgs[i].getAttribute('src') || '';
      var b64 = '';

      if (src.indexOf('data:application/octet-stream;base64,') === 0) {
        b64 = src.slice('data:application/octet-stream;base64,'.length);
      } else if (src.indexOf('data:;base64,') === 0) {
        b64 = src.slice('data:;base64,'.length);
      }

      if (b64) {
        var mime = sniffImageMime(b64);
        if (mime) {
          imgs[i].src = 'data:' + mime + ';base64,' + b64;
        }
        // If mime is null (e.g. EMF/WMF), leave src broken — nothing we can do.
      }
    }
  }

  /**
   * Returns the image MIME type by reading the first few bytes of
   * a base64-encoded image, or null if the format is unrecognised.
   */
  function sniffImageMime(b64) {
    try {
      // Decode only the first 16 bytes (22 base64 chars covers 16 bytes safely)
      var raw = atob(b64.substring(0, 24));
      var b = function (n) { return raw.charCodeAt(n); };

      // PNG  89 50 4E 47
      if (b(0) === 0x89 && b(1) === 0x50 && b(2) === 0x4E && b(3) === 0x47) return 'image/png';
      // JPEG FF D8 FF
      if (b(0) === 0xFF && b(1) === 0xD8 && b(2) === 0xFF)                   return 'image/jpeg';
      // GIF  47 49 46 38
      if (b(0) === 0x47 && b(1) === 0x49 && b(2) === 0x46 && b(3) === 0x38)  return 'image/gif';
      // WebP RIFF????WEBP
      if (b(0) === 0x52 && b(1) === 0x49 && b(2) === 0x46 && b(3) === 0x46 &&
          b(8) === 0x57 && b(9) === 0x45 && b(10) === 0x42 && b(11) === 0x50) return 'image/webp';
      // BMP  42 4D
      if (b(0) === 0x42 && b(1) === 0x4D)                                    return 'image/bmp';
      // TIFF 49 49 2A 00  or  4D 4D 00 2A
      if ((b(0) === 0x49 && b(1) === 0x49 && b(2) === 0x2A && b(3) === 0x00) ||
          (b(0) === 0x4D && b(1) === 0x4D && b(2) === 0x00 && b(3) === 0x2A)) return 'image/tiff';
      // SVG  starts with '<'
      if (b(0) === 0x3C)                                                      return 'image/svg+xml';
    } catch (e) { /* ignore decode errors */ }
    return null; // EMF/WMF/unknown — browser can't render anyway
  }

  /* ------------------------------------------------------------------ */
  /*  XLSX / PPTX — server-side conversion                               */
  /* ------------------------------------------------------------------ */
  function loadViaBackend(vpath, ext) {
    showLoading('Converting document…');

    fetch('/api/officepreview/convert?file=' + encodeURIComponent(vpath))
      .then(function (res) { return res.json(); })
      .then(function (data) {
        if (data.error) {
          showError(data.error);
          return;
        }
        switch (data.type) {
          case 'xlsx': renderXlsx(data); break;
          case 'pptx': renderPptx(data); break;
          default:     showError('Unsupported document type: ' + data.type);
        }
      })
      .catch(function (err) {
        showError('Failed to load document: ' + err.message);
      });
  }

  /* ------------------------------------------------------------------ */
  /*  XLSX Renderer                                                       */
  /* ------------------------------------------------------------------ */
  function renderXlsx(data) {
    sheetData = data;
    currentSheet = 0;

    const content = document.getElementById('ov-content');
    content.innerHTML =
      '<div id="ov-xlsx-wrap">' +
        '<div id="ov-xlsx-table-scroll"></div>' +
        '<div class="ov-xlsx-sheet-tabs" id="ov-sheet-tabs"></div>' +
      '</div>';

    buildSheetTabs(data.sheets);
    showSheet(0);
  }

  function buildSheetTabs(sheets) {
    const tabs = document.getElementById('ov-sheet-tabs');
    if (!tabs) return;
    tabs.innerHTML = '';
    sheets.forEach(function (sh, i) {
      const tab = document.createElement('button');
      tab.className = 'ov-sheet-tab' + (i === 0 ? ' active' : '');
      tab.textContent = sh.name || ('Sheet ' + (i + 1));
      tab.addEventListener('click', function () {
        showSheet(i);
      });
      tabs.appendChild(tab);
    });
  }

  function showSheet(idx) {
    currentSheet = idx;
    const tabs = document.querySelectorAll('.ov-sheet-tab');
    tabs.forEach(function (t, i) {
      t.classList.toggle('active', i === idx);
    });

    const scroll = document.getElementById('ov-xlsx-table-scroll');
    if (!scroll) return;

    const sheet = sheetData.sheets[idx];
    if (!sheet) return;

    const MAX_ROWS = 2000;
    const MAX_COLS = 200;
    const rows = sheet.rows || [];
    const colWidths = sheet.colWidths || [];
    const maxRow = Math.min(sheet.maxRow, MAX_ROWS - 1);
    const maxCol = Math.min(sheet.maxCol, MAX_COLS - 1);

    // Build merge lookup: "r,c" → {r2,c2}
    const mergeMap = {};
    const mergeSkip = {};
    (sheet.merges || []).forEach(function (m) {
      mergeMap[m.r1 + ',' + m.c1] = m;
      for (let r = m.r1; r <= m.r2; r++) {
        for (let c = m.c1; c <= m.c2; c++) {
          if (r !== m.r1 || c !== m.c1) {
            mergeSkip[r + ',' + c] = true;
          }
        }
      }
    });

    // Build colgroup widths (characters → approximate px)
    const COL_CHAR_PX = 8; // ~8px per character width unit
    let html = '<table class="ov-xlsx-table"><colgroup>';
    html += '<col style="width:36px">'; // row numbers
    for (let c = 0; c <= maxCol; c++) {
      const w = Math.round((colWidths[c] || 8.43) * COL_CHAR_PX);
      html += '<col style="width:' + w + 'px">';
    }
    html += '</colgroup><thead><tr><th></th>';

    // Column headers: A, B, C...
    for (let c = 0; c <= maxCol; c++) {
      html += '<th>' + colIndexToLetters(c) + '</th>';
    }
    html += '</tr></thead><tbody>';

    // Data rows
    for (let r = 0; r <= maxRow; r++) {
      html += '<tr><td class="ov-row-hdr">' + (r + 1) + '</td>';
      for (let c = 0; c <= maxCol; c++) {
        const key = r + ',' + c;
        if (mergeSkip[key]) continue;

        const row = rows[r] || [];
        const cell = row[c] || {};
        const m = mergeMap[key];

        let attrs = '';
        if (m) {
          const rowspan = m.r2 - m.r1 + 1;
          const colspan = m.c2 - m.c1 + 1;
          if (rowspan > 1) attrs += ' rowspan="' + rowspan + '"';
          if (colspan > 1) attrs += ' colspan="' + colspan + '"';
        }

        let style = '';
        const styles = [];
        if (cell.bg) styles.push('background:' + cell.bg);
        if (cell.fg) styles.push('color:'       + cell.fg);
        if (cell.bold)   styles.push('font-weight:700');
        if (cell.italic) styles.push('font-style:italic');
        if (cell.under)  styles.push('text-decoration:underline');
        if (cell.align)  styles.push('text-align:' + cell.align);
        else if (cell.t === 'n') styles.push('text-align:right');
        if (styles.length) style = ' style="' + styles.join(';') + '"';

        const val = cell.v != null ? escapeHtml(String(cell.v)) : '';
        html += '<td' + attrs + style + '>' + val + '</td>';
      }
      html += '</tr>';
    }

    if (maxRow < (sheet.maxRow || 0)) {
      html += '<tr><td class="ov-row-hdr">…</td><td colspan="' +
        (maxCol + 1) + '" style="color:#999;font-style:italic">Showing first ' +
        MAX_ROWS + ' rows</td></tr>';
    }

    html += '</tbody></table>';
    scroll.innerHTML = html;
  }

  function colIndexToLetters(idx) {
    let s = '';
    let n = idx + 1;
    while (n > 0) {
      n--;
      s = String.fromCharCode(65 + (n % 26)) + s;
      n = Math.floor(n / 26);
    }
    return s;
  }

  /* ------------------------------------------------------------------ */
  /*  PPTX Renderer                                                       */
  /* ------------------------------------------------------------------ */
  function renderPptx(data) {
    slideData   = data;
    totalSlides = (data.slides || []).length;
    currentSlide = 0;

    const slideW = data.width  || 9144000;
    const slideH = data.height || 6858000;
    const ratio  = slideW / slideH;

    const content = document.getElementById('ov-content');
    content.innerHTML =
      '<div id="ov-pptx-wrap">' +
        '<div id="ov-slide-panel"></div>' +
        '<div id="ov-slide-main">' +
          '<div id="ov-slide-view"></div>' +
          '<div id="ov-slide-nav">' +
            '<button class="ov-nav-btn" id="ov-btn-prev" title="Previous">&#8592;</button>' +
            '<span id="ov-slide-counter">1 / ' + totalSlides + '</span>' +
            '<button class="ov-nav-btn" id="ov-btn-next" title="Next">&#8594;</button>' +
          '</div>' +
        '</div>' +
      '</div>';

    // Set CSS slide ratio variable for thumbnails
    document.documentElement.style.setProperty('--slide-ratio', ratio.toFixed(4));

    // Build thumbnails
    buildSlideThumbs(data.slides, slideW, slideH);

    // Navigation
    document.getElementById('ov-btn-prev').addEventListener('click', function () {
      if (currentSlide > 0) showSlide(currentSlide - 1);
    });
    document.getElementById('ov-btn-next').addEventListener('click', function () {
      if (currentSlide < totalSlides - 1) showSlide(currentSlide + 1);
    });

    // Keyboard navigation
    document.addEventListener('keydown', pptxKeyNav);

    // Initial render
    showSlide(0);

    // Handle window resize
    window.addEventListener('resize', function () {
      if (slideData) showSlide(currentSlide);
    });
  }

  function pptxKeyNav(e) {
    if (e.key === 'ArrowRight' || e.key === 'ArrowDown' || e.key === ' ') {
      if (currentSlide < totalSlides - 1) showSlide(currentSlide + 1);
      e.preventDefault();
    } else if (e.key === 'ArrowLeft' || e.key === 'ArrowUp') {
      if (currentSlide > 0) showSlide(currentSlide - 1);
      e.preventDefault();
    }
  }

  function buildSlideThumbs(slides, slideW, slideH) {
    const panel = document.getElementById('ov-slide-panel');
    if (!panel) return;

    slides.forEach(function (slide, i) {
      const wrap = document.createElement('div');
      wrap.className = 'ov-slide-thumb-wrap' + (i === 0 ? ' active' : '');
      wrap.dataset.idx = i;

      const frame = document.createElement('div');
      frame.className = 'ov-thumb-frame';

      // Render a mini version of the slide
      const thumbSlide = renderSlideToEl(slide, slideW, slideH, true);
      frame.appendChild(thumbSlide);
      wrap.appendChild(frame);

      const num = document.createElement('div');
      num.className = 'ov-slide-num';
      num.textContent = i + 1;
      wrap.appendChild(num);

      wrap.addEventListener('click', function () { showSlide(i); });
      panel.appendChild(wrap);
    });
  }

  function showSlide(idx) {
    currentSlide = idx;

    // Update nav
    const counter = document.getElementById('ov-slide-counter');
    const prevBtn = document.getElementById('ov-btn-prev');
    const nextBtn = document.getElementById('ov-btn-next');
    if (counter) counter.textContent = (idx + 1) + ' / ' + totalSlides;
    if (prevBtn) prevBtn.disabled = idx === 0;
    if (nextBtn) nextBtn.disabled = idx === totalSlides - 1;

    // Update active thumbnail
    document.querySelectorAll('.ov-slide-thumb-wrap').forEach(function (el) {
      el.classList.toggle('active', parseInt(el.dataset.idx) === idx);
    });

    // Scroll thumbnail into view
    const activeThumb = document.querySelector('.ov-slide-thumb-wrap.active');
    if (activeThumb) activeThumb.scrollIntoView({ block: 'nearest' });

    // Render main view
    const view = document.getElementById('ov-slide-view');
    const main = document.getElementById('ov-slide-main');
    if (!view || !main) return;

    const slide = slideData.slides[idx];
    const slideW = slideData.width  || 9144000;
    const slideH = slideData.height || 6858000;

    // Calculate available space
    const availW = main.clientWidth  - 32;
    const availH = main.clientHeight - 60; // leave room for nav
    const aspect = slideW / slideH;
    let renderW = availW;
    let renderH = Math.round(renderW / aspect);
    if (renderH > availH) {
      renderH = availH;
      renderW = Math.round(renderH * aspect);
    }
    renderW = Math.max(renderW, 200);
    renderH = Math.max(renderH, 150);

    view.style.width  = renderW + 'px';
    view.style.height = renderH + 'px';
    view.innerHTML = '';

    const slideEl = renderSlideToEl(slide, slideW, slideH, false, renderW, renderH);
    view.appendChild(slideEl);
  }

  /**
   * Renders a slide into a DOM element.
   * @param {Object}  slide
   * @param {number}  slideW  - slide width in EMU
   * @param {number}  slideH  - slide height in EMU
   * @param {boolean} isThumb - true for thumbnail mode (uses CSS scale)
   * @param {number}  [renderW] - target pixel width (only for non-thumb)
   * @param {number}  [renderH] - target pixel height (only for non-thumb)
   */
  function renderSlideToEl(slide, slideW, slideH, isThumb, renderW, renderH) {
    // EMU → pixel at 96dpi: 1 EMU = 1/914400 inch * 96 px/inch = 96/914400
    const EMU_TO_PX = 96 / 914400;
    const nativeW = Math.round(slideW * EMU_TO_PX); // e.g. 960 for 9144000
    const nativeH = Math.round(slideH * EMU_TO_PX); // e.g. 720 or 540

    const canvas = document.createElement('div');
    canvas.className = 'ov-slide-canvas';
    canvas.style.width  = nativeW + 'px';
    canvas.style.height = nativeH + 'px';
    canvas.style.background = slide.bg || '#ffffff';

    (slide.elements || []).forEach(function (el) {
      renderSlideElement(canvas, el, EMU_TO_PX, nativeW, nativeH);
    });

    if (isThumb) {
      // Use CSS scale so the thumb fills its parent via CSS
      const wrapper = document.createElement('div');
      wrapper.style.width  = '100%';
      wrapper.style.height = '100%';
      wrapper.style.overflow = 'hidden';
      wrapper.style.position = 'relative';
      // Scale canvas to fit 100% of wrapper width
      canvas.style.transformOrigin = 'top left';
      canvas.style.position = 'absolute';
      // The actual scaling is applied dynamically by CSS; use a scale var trick
      canvas.dataset.nativeW = nativeW;
      canvas.dataset.nativeH = nativeH;
      wrapper.appendChild(canvas);
      // Apply scale after DOM insertion via a micro-task
      requestAnimationFrame(function () {
        const w = wrapper.offsetWidth;
        if (w > 0) {
          const s = w / nativeW;
          canvas.style.transform = 'scale(' + s + ')';
          wrapper.style.height = Math.round(nativeH * s) + 'px';
        }
      });
      return wrapper;
    } else {
      // Scale to exact target size
      const scale = renderW / nativeW;
      const wrapper = document.createElement('div');
      wrapper.style.width    = renderW + 'px';
      wrapper.style.height   = renderH + 'px';
      wrapper.style.overflow = 'hidden';
      wrapper.style.position = 'relative';
      canvas.style.transformOrigin = 'top left';
      canvas.style.transform = 'scale(' + scale + ')';
      canvas.style.position  = 'absolute';
      wrapper.appendChild(canvas);
      return wrapper;
    }
  }

  function renderSlideElement(parent, el, emuToPx, nativeW, nativeH) {
    const div = document.createElement('div');
    div.className = 'ov-slide-el';
    div.style.left   = Math.round(el.x * emuToPx) + 'px';
    div.style.top    = Math.round(el.y * emuToPx) + 'px';
    div.style.width  = Math.round(el.w * emuToPx) + 'px';
    div.style.height = Math.round(el.h * emuToPx) + 'px';

    if (el.type === 'image' && el.src) {
      const img = document.createElement('img');
      img.src = el.src;
      img.className = 'ov-slide-el-img';
      img.alt = '';
      div.appendChild(img);

    } else if (el.type === 'text' && el.paragraphs) {
      div.className += ' ov-slide-el-text';

      // Vertical alignment
      switch (el.vAlign) {
        case 'ctr': div.style.justifyContent = 'center'; break;
        case 'b':   div.style.justifyContent = 'flex-end'; break;
        default:    div.style.justifyContent = 'flex-start';
      }

      el.paragraphs.forEach(function (para) {
        const p = document.createElement('p');
        if (para.align === 'center') p.style.textAlign = 'center';
        else if (para.align === 'right')   p.style.textAlign = 'right';
        else if (para.align === 'justify') p.style.textAlign = 'justify';

        (para.runs || []).forEach(function (run) {
          if (run.text === '\n') {
            p.appendChild(document.createElement('br'));
            return;
          }
          const span = document.createElement('span');
          span.textContent = run.text;
          if (run.bold)   span.style.fontWeight = '700';
          if (run.italic) span.style.fontStyle  = 'italic';
          if (run.under)  span.style.textDecoration = 'underline';
          if (run.size && run.size > 0) {
            // size is in hundredths of a point; 1pt = 4/3 CSS px at 96dpi
            span.style.fontSize = (run.size / 100 * 4 / 3).toFixed(1) + 'px';
          }
          if (run.color)  span.style.color = run.color;
          if (run.font)   span.style.fontFamily = cssQuoteFontFamily(run.font);
          p.appendChild(span);
        });
        div.appendChild(p);
      });

    } else if (el.type === 'shape' && el.fill) {
      div.style.background = el.fill;
    }

    if (div.children.length > 0 || el.type === 'shape') {
      parent.appendChild(div);
    }
  }

  /* ------------------------------------------------------------------ */
  /*  UI helpers                                                          */
  /* ------------------------------------------------------------------ */
  function showLoading(msg) {
    const content = document.getElementById('ov-content');
    content.innerHTML =
      '<div id="ov-loading">' +
        '<div class="ov-spinner"></div>' +
        '<div>' + escapeHtml(msg || 'Loading…') + '</div>' +
      '</div>';
  }

  function showError(msg) {
    const content = document.getElementById('ov-content');
    content.innerHTML =
      '<div id="ov-loading">' +
        '<div class="ov-error-msg">&#9888; ' + escapeHtml(msg) + '</div>' +
      '</div>';
  }

  function escapeHtml(s) {
    return String(s)
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;');
  }

  function cssQuoteFontFamily(name) {
    if (/[\s,]/.test(name)) return "'" + name.replace(/'/g, "\\'") + "'";
    return name;
  }

  /* ------------------------------------------------------------------ */
  /*  Bootstrap                                                           */
  /* ------------------------------------------------------------------ */
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }

})();
