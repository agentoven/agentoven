/**
 * Media helpers — attachments for session messages and cascaded voice.
 *
 * Attachments (images, PDFs, audio, video) become typed `content_parts` on a
 * session message:
 *
 * ```ts
 * await pro.sendSessionMessage('vision-agent', session.id, {
 *   content: 'What is in these?',
 *   attachments: [await Attachment.fromPath('chart.png'), await Attachment.fromPath('report.pdf')],
 * });
 * ```
 *
 * Cascaded voice (speech in, speech out) goes through `invoke`:
 *
 * ```ts
 * const r = await pro.invoke('support-agent', { audio: await audioFromPath('question.wav'), voiceOutput: true });
 * console.log(r.transcript, r.response);
 * await saveAudio(r.audio!, 'answer'); // answer.mp3
 * ```
 *
 * Every part carries a MIME type — the server needs it to pick the provider
 * wire format. It is inferred from the file name, URL or the bytes' magic
 * number; pass `mimeType` when it cannot be. Nothing here needs Node except the
 * `fromPath` / `saveAudio` helpers, so the rest works in a browser.
 */

import { fromBase64, toBase64 } from './bytes.js';

/** `file` is used for PDFs and other documents. */
export type ContentPartType = 'image' | 'file' | 'audio' | 'video';

/** Where a part's bytes are. Exactly one of `data` (base64) or `url`. */
export interface MediaRef {
  mime_type: string;
  data?: string;
  url?: string;
  name?: string;
}

/** One typed media part of a session message (`content_parts`). */
export interface ContentPart {
  type: ContentPartType;
  media: MediaRef;
}

export interface AttachmentOptions {
  /** Overrides inference. Needed for bytes of a format the SDK cannot recognise. */
  mimeType?: string;
  /** The original file name, for display and provider file uploads. */
  name?: string;
}

// Pinned spellings the server accepts (platform MIME tables disagree, e.g.
// .wav is audio/x-wav on some).
const EXT_MIME: Record<string, string> = {
  png: 'image/png',
  jpg: 'image/jpeg',
  jpeg: 'image/jpeg',
  gif: 'image/gif',
  webp: 'image/webp',
  pdf: 'application/pdf',
  wav: 'audio/wav',
  mp3: 'audio/mpeg',
  m4a: 'audio/mp4',
  ogg: 'audio/ogg',
  oga: 'audio/ogg',
  flac: 'audio/flac',
  mp4: 'video/mp4',
  mov: 'video/quicktime',
  mpeg: 'video/mpeg',
  mpg: 'video/mpeg',
  avi: 'video/x-msvideo',
};

type Prefer = 'audio' | 'video' | undefined;

/** Identify common media formats from their leading bytes, or `undefined`. */
export function sniffMimeType(data: Uint8Array, prefer?: Prefer): string | undefined {
  const at = (i: number, s: string): boolean => {
    for (let k = 0; k < s.length; k++) if (data[i + k] !== s.charCodeAt(k)) return false;
    return true;
  };
  if (data.length >= 8 && data[0] === 0x89 && at(1, 'PNG\r\n') && data[6] === 0x1a && data[7] === 0x0a) return 'image/png';
  if (data[0] === 0xff && data[1] === 0xd8 && data[2] === 0xff) return 'image/jpeg';
  if (at(0, 'GIF87a') || at(0, 'GIF89a')) return 'image/gif';
  if (at(0, 'RIFF') && at(8, 'WEBP')) return 'image/webp';
  if (at(0, '%PDF')) return 'application/pdf';
  if (at(0, 'RIFF') && at(8, 'WAVE')) return 'audio/wav';
  if (at(0, 'RIFF') && at(8, 'AVI ')) return 'video/x-msvideo';
  if (at(0, 'ID3') || (data[0] === 0xff && (data[1] === 0xfb || data[1] === 0xf3 || data[1] === 0xf2))) return 'audio/mpeg';
  if (at(0, 'OggS')) return 'audio/ogg';
  if (at(0, 'fLaC')) return 'audio/flac';
  if (data[0] === 0x1a && data[1] === 0x45 && data[2] === 0xdf && data[3] === 0xa3) return prefer === 'audio' ? 'audio/webm' : 'video/webm';
  if (at(4, 'ftyp')) {
    if (at(8, 'M4A ') || at(8, 'M4B ')) return 'audio/mp4';
    if (at(8, 'qt  ')) return 'video/quicktime';
    return prefer === 'audio' ? 'audio/mp4' : 'video/mp4';
  }
  return undefined;
}

/** Guess a MIME type from a file name or URL, or `undefined`. */
export function guessMimeType(name: string, prefer?: Prefer): string | undefined {
  const path = name.split(/[?#]/, 1)[0];
  const dot = path.lastIndexOf('.');
  if (dot < 0 || path.slice(dot).includes('/')) return undefined;
  const ext = path.slice(dot + 1).toLowerCase();
  if (ext === 'webm') return prefer === 'audio' ? 'audio/webm' : 'video/webm';
  return EXT_MIME[ext];
}

/** Drop parameters (`audio/webm;codecs=opus` -> `audio/webm`): the server matches the bare type. */
export function normalizeMimeType(mime: string): string {
  return mime.split(';', 1)[0].trim().toLowerCase();
}

function partType(mime: string): ContentPartType {
  if (mime.startsWith('image/')) return 'image';
  if (mime.startsWith('audio/')) return 'audio';
  if (mime.startsWith('video/')) return 'video';
  return 'file';
}

const baseName = (path: string): string => path.split(/[\\/]/).pop() ?? path;

// Resolved at run time so the package has no hard Node dependency and the
// browser bundle never tries to include fs.
async function readFile(path: string): Promise<Uint8Array> {
  const spec = 'node:fs/promises';
  let fs: { readFile(p: string): Promise<Uint8Array> };
  try {
    fs = await import(/* @vite-ignore */ spec);
  } catch {
    throw new Error('reading a file needs Node; in a browser pass bytes (e.g. await file.arrayBuffer())');
  }
  try {
    return new Uint8Array(await fs.readFile(path));
  } catch (err) {
    throw new Error(`cannot read '${path}': ${(err as { message?: string }).message ?? err}`);
  }
}

/** Build the typed media parts of a session message. */
export const Attachment = {
  /** From raw bytes (e.g. `new Uint8Array(await file.arrayBuffer())`). */
  fromBytes(data: Uint8Array | ArrayBuffer, opts: AttachmentOptions = {}): ContentPart {
    const bytes = data instanceof Uint8Array ? data : new Uint8Array(data);
    const mime = opts.mimeType ?? (opts.name ? guessMimeType(opts.name) : undefined) ?? sniffMimeType(bytes);
    if (!mime) throw new Error('cannot tell the media type from these bytes; pass mimeType');
    const m = normalizeMimeType(mime);
    const media: MediaRef = { mime_type: m, data: toBase64(bytes) };
    if (opts.name) media.name = opts.name;
    return { type: partType(m), media };
  },

  /** From a file on disk (Node). The MIME type comes from the name, else the bytes. */
  async fromPath(path: string, opts: Pick<AttachmentOptions, 'mimeType'> = {}): Promise<ContentPart> {
    const bytes = await readFile(path);
    const name = baseName(path);
    const mime = opts.mimeType ?? guessMimeType(name) ?? sniffMimeType(bytes);
    if (!mime) throw new Error(`cannot tell the media type of '${name}'; pass mimeType`);
    return Attachment.fromBytes(bytes, { mimeType: mime, name });
  },

  /** From a URL the provider (or the control plane) can fetch. */
  fromUrl(url: string, opts: AttachmentOptions = {}): ContentPart {
    const mime = opts.mimeType ?? guessMimeType(url);
    if (!mime) throw new Error(`cannot tell the media type of '${url}' from its extension; pass mimeType`);
    const m = normalizeMimeType(mime);
    const media: MediaRef = { mime_type: m, url };
    if (opts.name) media.name = opts.name;
    return { type: partType(m), media };
  },
};

// ── Cascaded voice ───────────────────────────────────────────────────────────

/** Spoken input for `invoke({ audio })`: base64 audio and its MIME type. */
export interface AudioInput {
  /** Base64 audio. */
  data: string;
  /** `audio/wav`, `audio/mpeg`, `audio/mp4`, `audio/webm`, `audio/ogg` or `audio/flac`. */
  mimeType: string;
}

function makeAudio(data: string, mime: string): AudioInput {
  if (!data) throw new Error('audio data is empty');
  const m = normalizeMimeType(mime);
  if (!m.startsWith('audio/')) throw new Error(`audio mimeType must be audio/*, got '${m}'`);
  return { data, mimeType: m };
}

/** From raw bytes (e.g. a `MediaRecorder` blob). The format is sniffed unless `mimeType` is given. */
export function audioFromBytes(data: Uint8Array | ArrayBuffer, mimeType?: string): AudioInput {
  const bytes = data instanceof Uint8Array ? data : new Uint8Array(data);
  const mime = mimeType ?? sniffMimeType(bytes, 'audio');
  if (!mime) throw new Error('cannot tell the audio format from these bytes; pass mimeType');
  return makeAudio(toBase64(bytes), mime);
}

/** From base64 text you already have. */
export function audioFromBase64(data: string, mimeType: string): AudioInput {
  return makeAudio(data, mimeType);
}

/** From an audio file on disk (Node). */
export async function audioFromPath(path: string, mimeType?: string): Promise<AudioInput> {
  const bytes = await readFile(path);
  const name = baseName(path);
  const mime = mimeType ?? guessMimeType(name, 'audio') ?? sniffMimeType(bytes, 'audio');
  if (!mime) throw new Error(`cannot tell the audio format of '${name}'; pass mimeType`);
  return makeAudio(toBase64(bytes), mime);
}

/** Audio the server returned (a spoken reply), decoded. */
export interface AudioClip {
  data: Uint8Array;
  mimeType: string;
  /** A file extension for the clip, e.g. `.mp3` (`.bin` if unknown). */
  extension: string;
}

const AUDIO_EXT: Record<string, string> = {
  'audio/mpeg': '.mp3',
  'audio/mp3': '.mp3',
  'audio/wav': '.wav',
  'audio/x-wav': '.wav',
  'audio/mp4': '.m4a',
  'audio/ogg': '.ogg',
  'audio/webm': '.webm',
  'audio/flac': '.flac',
  'audio/aac': '.aac',
  'audio/opus': '.opus',
};

/** @internal decode the wire `{data, mime_type}` of a reply. */
export function audioClipFromWire(wire: { data?: string; mime_type?: string }): AudioClip {
  const mimeType = wire.mime_type ?? '';
  return {
    data: wire.data ? fromBase64(wire.data) : new Uint8Array(0),
    mimeType,
    extension: AUDIO_EXT[normalizeMimeType(mimeType)] ?? '.bin',
  };
}

/**
 * Write a clip to disk (Node) and return the path. A `path` with no extension
 * gets the one that fits the clip's format.
 */
export async function saveAudio(clip: AudioClip, path: string): Promise<string> {
  const spec = 'node:fs/promises';
  let fs: { writeFile(p: string, d: Uint8Array): Promise<void> };
  try {
    fs = await import(/* @vite-ignore */ spec);
  } catch {
    throw new Error('saving a file needs Node; in a browser play the clip via a Blob instead');
  }
  const out = /\.[^./\\]+$/.test(path) ? path : path + clip.extension;
  await fs.writeFile(out, clip.data);
  return out;
}
