/**
 * Provider modalities — typed config helpers.
 *
 * A *modality* is something an agent can take in or give out beyond text:
 * `image`, `pdf`, `video`, `audio` (cascaded voice: speech-to-text in,
 * text-to-speech out) and `realtime` (live speech-to-speech). Modalities belong
 * to the **provider**: an agent has exactly the modalities of its own
 * provider. Text is always on and has no entry.
 *
 * Nothing needs to be set — defaults come from the provider driver and the
 * model catalog. Use the builder to switch one off, or to switch
 * image/pdf/video on for a gateway the catalog does not know:
 *
 * ```ts
 * await pro.createProvider({
 *   name: 'openai', kind: 'openai', api_key: '...',
 *   modalities: modalities().pdf(false).video(true).audio({ sttModel: 'whisper-1', ttsModel: 'tts-1' }),
 * });
 * ```
 *
 * On update the object is merged per modality; `null` deletes a key (or a whole
 * modality) and puts it back on its default:
 *
 * ```ts
 * await pro.updateProvider('openai', { modalities: modalities().clear('pdf').audio({ ttsModel: null }) });
 * ```
 *
 * The rules the server enforces are checked client-side too, so a typo fails
 * before the request is sent.
 */

/** The modalities that take configuration. Text is always on. */
export type ModalityName = 'image' | 'pdf' | 'video' | 'audio' | 'realtime';

/** Every modality in display order, as the server lists them. */
export const ALL_MODALITIES = ['text', 'image', 'pdf', 'video', 'audio', 'realtime'] as const;
export type AnyModality = (typeof ALL_MODALITIES)[number];

/** Settings of image, pdf and video: just on/off. */
export interface ToggleModality {
  enabled?: boolean;
}

export interface AudioModality {
  enabled?: boolean;
  stt_model?: string;
  tts_model?: string;
}

export interface RealtimeModality {
  enabled?: boolean;
  model?: string;
}

/** `config.modalities` as stored: wire format. */
export interface ModalitiesConfig {
  image?: ToggleModality;
  pdf?: ToggleModality;
  video?: ToggleModality;
  audio?: AudioModality;
  realtime?: RealtimeModality;
}

type Nullable<T> = { [K in keyof T]?: T[K] | null };

/** `config.modalities` on a provider update: `null` deletes a key or a whole modality. */
export interface ModalitiesUpdate {
  image?: Nullable<ToggleModality> | null;
  pdf?: Nullable<ToggleModality> | null;
  video?: Nullable<ToggleModality> | null;
  audio?: Nullable<AudioModality> | null;
  realtime?: Nullable<RealtimeModality> | null;
}

/** Thrown for an invalid modalities config or an unknown modality name. */
export class ModalitiesError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'ModalitiesError';
  }
}

const SETTINGS: Record<ModalityName, readonly string[]> = {
  image: ['enabled'],
  pdf: ['enabled'],
  video: ['enabled'],
  audio: ['enabled', 'stt_model', 'tts_model'],
  realtime: ['enabled', 'model'],
};

const isObject = (v: unknown): v is Record<string, unknown> =>
  typeof v === 'object' && v !== null && !Array.isArray(v);

function typeMessage(name: string, key: string): string {
  return key === 'enabled'
    ? `config.modalities.${name}.enabled must be true or false`
    : `config.modalities.${name}.${key} must be a string`;
}

/**
 * Check a `config.modalities` object the way the server does and return a
 * copy. Throws {@link ModalitiesError} for an unknown modality, an unknown
 * setting, or a value of the wrong type. `allowClear` permits `null`, which
 * only means something on a provider *update*.
 */
export function validateModalities(
  value: unknown,
  opts: { allowClear?: boolean } = {},
): ModalitiesUpdate {
  const allowClear = opts.allowClear ?? false;
  if (!isObject(value)) throw new ModalitiesError('config.modalities must be an object');
  const out: Record<string, unknown> = {};
  for (const name of Object.keys(value).sort()) {
    if (!(name in SETTINGS)) {
      const hint = name === 'text' ? 'text is always on and takes no entry; ' : '';
      throw new ModalitiesError(
        `config.modalities.${name}: ${hint}unknown modality (use image, pdf, video, audio, realtime)`,
      );
    }
    const allowed = SETTINGS[name as ModalityName];
    const entry = value[name];
    if (entry === null || entry === undefined) {
      if (entry === undefined) continue; // an unset optional property
      if (!allowClear) throw new ModalitiesError(`config.modalities.${name} must be an object`);
      out[name] = null;
      continue;
    }
    if (!isObject(entry)) throw new ModalitiesError(`config.modalities.${name} must be an object`);
    const clean: Record<string, unknown> = {};
    for (const key of Object.keys(entry).sort()) {
      if (!allowed.includes(key)) {
        throw new ModalitiesError(
          `config.modalities.${name}.${key}: unknown setting (allowed: ${allowed.join(', ')})`,
        );
      }
      const v = entry[key];
      if (v === undefined) continue;
      if (v === null) {
        if (!allowClear) throw new ModalitiesError(typeMessage(name, key));
        clean[key] = null;
      } else if (key === 'enabled' ? typeof v !== 'boolean' : typeof v !== 'string') {
        throw new ModalitiesError(typeMessage(name, key));
      } else {
        clean[key] = v;
      }
    }
    out[name] = clean;
  }
  return out as ModalitiesUpdate;
}

/** Options of {@link ModalitiesBuilder.audio}. `null` deletes the setting on update. */
export interface AudioOptions {
  enabled?: boolean | null;
  sttModel?: string | null;
  ttsModel?: string | null;
}

/** Options of {@link ModalitiesBuilder.realtime}. `null` deletes the setting on update. */
export interface RealtimeOptions {
  enabled?: boolean | null;
  model?: string | null;
}

/** Fluent builder for `config.modalities`. Start with {@link modalities}. */
export class ModalitiesBuilder {
  private readonly entries: Record<string, unknown> = {};

  private set(name: ModalityName, entry: unknown): this {
    this.entries[name] = entry;
    return this;
  }

  /** Turn image input on or off. */
  image(enabled: boolean): this {
    return this.set('image', { enabled });
  }

  /** Turn PDF input on or off. */
  pdf(enabled: boolean): this {
    return this.set('pdf', { enabled });
  }

  /** Turn video input on or off. */
  video(enabled: boolean): this {
    return this.set('video', { enabled });
  }

  /** Cascaded voice. A bare boolean is shorthand for `{ enabled }`. */
  audio(options: boolean | AudioOptions): this {
    if (typeof options === 'boolean') return this.set('audio', { enabled: options });
    const e: Record<string, unknown> = {};
    if (options.enabled !== undefined) e.enabled = options.enabled;
    if (options.sttModel !== undefined) e.stt_model = options.sttModel;
    if (options.ttsModel !== undefined) e.tts_model = options.ttsModel;
    return this.set('audio', e);
  }

  /** Live speech-to-speech. A bare boolean is shorthand for `{ enabled }`. */
  realtime(options: boolean | RealtimeOptions): this {
    if (typeof options === 'boolean') return this.set('realtime', { enabled: options });
    const e: Record<string, unknown> = {};
    if (options.enabled !== undefined) e.enabled = options.enabled;
    if (options.model !== undefined) e.model = options.model;
    return this.set('realtime', e);
  }

  /** On update, delete a whole modality's settings (back to its default). */
  clear(name: ModalityName): this {
    return this.set(name, null);
  }

  /**
   * The validated `config.modalities` object. Allows `null` (clear), so use it
   * for updates; `createProvider` re-validates without it.
   */
  build(): ModalitiesUpdate {
    return validateModalities(this.entries, { allowClear: true });
  }
}

/** Start a modalities config: `modalities().pdf(false).audio({ sttModel: 'whisper-1' })`. */
export function modalities(): ModalitiesBuilder {
  return new ModalitiesBuilder();
}

/** Accepts a builder or a plain wire-format object and returns the validated object. */
export function resolveModalities(
  value: ModalitiesBuilder | ModalitiesUpdate | ModalitiesConfig,
  opts: { allowClear?: boolean } = {},
): ModalitiesUpdate {
  return validateModalities(value instanceof ModalitiesBuilder ? value.build() : value, opts);
}

/**
 * Whether a provider or agent card offers `modality`. Text is always on. Works
 * on anything with a `modalities` list, e.g. a `Provider` or an `AgentCard`.
 * Throws {@link ModalitiesError} for a name that is not a modality.
 */
export function supportsModality(subject: { modalities?: string[] }, modality: AnyModality): boolean {
  if (!(ALL_MODALITIES as readonly string[]).includes(modality)) {
    throw new ModalitiesError(`unknown modality '${modality}' (use ${ALL_MODALITIES.join(', ')})`);
  }
  return modality === 'text' || (subject.modalities ?? []).includes(modality);
}
