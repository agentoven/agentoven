import { describe, expect, it } from 'vitest';
import {
  ModalitiesError,
  modalities,
  resolveModalities,
  supportsModality,
  validateModalities,
} from '../src/modalities.js';

describe('modalities builder', () => {
  it('builds the server shape', () => {
    const cfg = modalities()
      .image(true)
      .pdf(false)
      .video(true)
      .audio({ enabled: true, sttModel: 'whisper-1', ttsModel: 'tts-1' })
      .realtime({ enabled: true, model: 'gpt-realtime' })
      .build();
    expect(cfg).toEqual({
      image: { enabled: true },
      pdf: { enabled: false },
      video: { enabled: true },
      audio: { enabled: true, stt_model: 'whisper-1', tts_model: 'tts-1' },
      realtime: { enabled: true, model: 'gpt-realtime' },
    });
  });

  it('leaves unset things out and accepts a boolean shorthand', () => {
    expect(modalities().build()).toEqual({});
    expect(modalities().audio({ ttsModel: 'tts-1' }).build()).toEqual({ audio: { tts_model: 'tts-1' } });
    expect(modalities().audio(false).realtime(true).build()).toEqual({
      audio: { enabled: false },
      realtime: { enabled: true },
    });
  });

  it('clears a whole modality or one key with null', () => {
    expect(modalities().clear('pdf').audio({ sttModel: null, ttsModel: 'tts-1' }).build()).toEqual({
      pdf: null,
      audio: { stt_model: null, tts_model: 'tts-1' },
    });
  });

  it('is refused where there is nothing to delete', () => {
    expect(() => resolveModalities(modalities().clear('pdf'), { allowClear: false })).toThrow(/pdf must be an object/);
    expect(() => validateModalities({ audio: { stt_model: null } })).toThrow(/stt_model must be a string/);
    expect(() => validateModalities({ image: { enabled: null } })).toThrow(/enabled must be true or false/);
  });
});

describe('validateModalities mirrors the server', () => {
  it('rejects text with a hint, and unknown names', () => {
    expect(() => validateModalities({ text: { enabled: true } })).toThrow(/text is always on and takes no entry/);
    expect(() => validateModalities({ smell: {} })).toThrow(
      /config\.modalities\.smell: unknown modality \(use image, pdf, video, audio, realtime\)/,
    );
  });

  it('rejects unknown settings, listing what is allowed', () => {
    expect(() => validateModalities({ image: { model: 'x' } })).toThrow(/image\.model: unknown setting \(allowed: enabled\)/);
    expect(() => validateModalities({ audio: { model: 'x' } })).toThrow(
      /audio\.model: unknown setting \(allowed: enabled, stt_model, tts_model\)/,
    );
    expect(() => validateModalities({ realtime: { stt_model: 'x' } })).toThrow(/allowed: enabled, model/);
  });

  it('rejects wrong types', () => {
    expect(() => validateModalities({ image: { enabled: 'yes' } })).toThrow(/image\.enabled must be true or false/);
    expect(() => validateModalities({ image: { enabled: 1 } })).toThrow(/image\.enabled must be true or false/);
    expect(() => validateModalities({ audio: { stt_model: 3 } })).toThrow(/audio\.stt_model must be a string/);
    expect(() => validateModalities({ realtime: true })).toThrow(/realtime must be an object/);
    expect(() => validateModalities(['image'])).toThrow(/config\.modalities must be an object/);
    expect(() => validateModalities(null)).toThrow(ModalitiesError);
  });

  it('throws ModalitiesError, an Error', () => {
    try {
      validateModalities({ nope: {} });
    } catch (e) {
      expect(e).toBeInstanceOf(ModalitiesError);
      expect(e).toBeInstanceOf(Error);
      expect((e as Error).name).toBe('ModalitiesError');
    }
  });
});

describe('supportsModality', () => {
  const card = { modalities: ['text', 'image', 'audio'] };
  it('answers from the list, with text always on', () => {
    expect(supportsModality(card, 'audio')).toBe(true);
    expect(supportsModality(card, 'pdf')).toBe(false);
    expect(supportsModality({}, 'text')).toBe(true);
    expect(supportsModality({}, 'image')).toBe(false);
  });
  it('rejects a misspelt name', () => {
    expect(() => supportsModality(card, 'audo' as never)).toThrow(/unknown modality 'audo'/);
  });
});
