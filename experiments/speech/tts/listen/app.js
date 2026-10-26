const byId = (id) => document.getElementById(id);
const storageKey = 'opal-tts-review-v1';
function loadReviews(fallback = {}) {
  try {
    const value = JSON.parse(localStorage.getItem(storageKey) || '{}');
    return value && !Array.isArray(value) && typeof value === 'object' ? value : fallback;
  } catch { return fallback; }
}
let reviews = loadReviews();
let catalogue;
function element(tag, text, className) {
  const node = document.createElement(tag);
  if (text !== undefined) node.textContent = text;
  if (className) node.className = className;
  return node;
}
function option(value, label) {
  const item = element('option', label);
  item.value = value;
  return item;
}
function save(key, field, value) {
  reviews = loadReviews(reviews);
  reviews[key] = { ...(reviews[key] || {}), [field]: value };
  try { localStorage.setItem(storageKey, JSON.stringify(reviews)); }
  catch { byId('storage').textContent = 'Browser storage is unavailable. Export your review before you close this page.'; }
}
function render() {
  document.querySelectorAll('audio').forEach((audio) => audio.pause());
  const prompt = byId('prompt').value;
  const engine = byId('engine').value;
  const hide = byId('hide').checked;
  const promptRecord = catalogue.runs.flatMap((run) => run.prompts).find((item) => item.id === prompt);
  byId('text').textContent = promptRecord?.text || '';
  byId('samples').replaceChildren();
  let count = 0;
  for (const run of catalogue.runs) {
    for (const sample of run.samples || []) {
      if (sample.prompt !== prompt || (engine && run.engine !== engine)) continue;
      count += 1;
      const key = `${run.id}/${sample.file}`;
      const card = element('article');
      const title = hide ? `Sample ${count}` : `${run.engine} · ${sample.voice}`;
      card.append(element('h2', title));
      const audio = element('audio');
      audio.controls = true;
      audio.preload = 'none';
      audio.src = `/audio/${encodeURIComponent(run.id)}/${encodeURIComponent(sample.file)}`;
      audio.setAttribute('aria-label', title);
      audio.addEventListener('play', () => document.querySelectorAll('audio').forEach((other) => {
        if (audio !== other) other.pause();
      }));
      audio.addEventListener('error', () => {
        byId('status').textContent = 'An audio file is unavailable. Check that WAV files were copied with the manifests.';
      });
      card.append(audio);
      if (!hide) card.append(element('p', `${sample.audio_seconds.toFixed(1)} s audio · first audio ${sample.first_audio_seconds.toFixed(2)} s · RTF ${sample.real_time_factor.toFixed(2)} · take ${sample.repeat}`, 'metrics'));
      const ratingLabel = element('label', 'Your choice ');
      const rating = element('select');
      rating.dataset.reviewKey = key;
      rating.dataset.reviewField = 'rating';
      for (const [value, label] of [['', 'Not reviewed'], ['keep', 'Keep this voice'], ['maybe', 'Compare again'], ['reject', 'Do not use']]) rating.append(option(value, label));
      rating.value = reviews[key]?.rating || '';
      rating.addEventListener('change', () => save(key, 'rating', rating.value));
      ratingLabel.append(rating);
      const notesLabel = element('label', 'Notes ');
      const notes = element('textarea');
      notes.dataset.reviewKey = key;
      notes.dataset.reviewField = 'notes';
      notes.rows = 2;
      notes.value = reviews[key]?.notes || '';
      notes.placeholder = 'Missing words, pronunciation, preferred persona…';
      notes.addEventListener('input', () => save(key, 'notes', notes.value));
      notesLabel.append(notes);
      card.append(ratingLabel, notesLabel);
      const transcript = catalogue.transcripts?.samples.find((item) => item.run === run.id && item.file === sample.file);
      if (transcript && !hide) {
        const details = element('details');
        details.append(element('summary', 'Parakeet transcript check'));
        details.append(element('p', transcript.transcript));
        details.append(element('small', 'One recognizer can be wrong. This is not a voice quality score.'));
        card.append(details);
      }
      byId('samples').append(card);
    }
  }
  byId('status').textContent = count
    ? `${count} samples. ${hide ? 'Names and timings are hidden. This is not a randomized blind trial.' : 'Choose a test text to compare voices.'}`
    : 'No samples for this engine and text. Select another combination.';
}
byId('export').addEventListener('click', () => {
  reviews = loadReviews(reviews);
  const data = {version: 1, exported_at: new Date().toISOString(), reviews};
  const url = URL.createObjectURL(new Blob([JSON.stringify(data, null, 2)], {type: 'application/json'}));
  const anchor = element('a');
  anchor.href = url;
  anchor.download = 'opal-tts-review.json';
  anchor.click();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
});
window.addEventListener('storage', (event) => {
  if (event.key !== storageKey) return;
  reviews = loadReviews(reviews);
  for (const field of document.querySelectorAll('[data-review-field]')) {
    if (field !== document.activeElement) field.value = reviews[field.dataset.reviewKey]?.[field.dataset.reviewField] || '';
  }
});
for (const id of ['prompt', 'engine', 'hide']) byId(id).addEventListener('change', render);
fetch('/catalog.json').then((response) => {
  if (!response.ok) throw new Error(`Catalogue request failed: ${response.status}`);
  return response.json();
}).then((data) => {
  catalogue = data;
  const prompts = new Map();
  for (const run of data.runs) for (const prompt of run.prompts) prompts.set(prompt.id, prompt);
  for (const prompt of prompts.values()) byId('prompt').append(option(prompt.id, prompt.label));
  for (const engine of new Set(data.runs.map((run) => run.engine))) byId('engine').append(option(engine, engine));
  if (prompts.has('introduction')) byId('prompt').value = 'introduction';
  render();
}).catch((error) => { byId('status').textContent = error.message; });
