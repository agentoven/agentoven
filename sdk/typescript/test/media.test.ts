import { mkdtemp, readFile, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';
import {
  Attachment,
  audioClipFromWire,
  audioFromBase64,
  audioFromBytes,
  audioFromPath,
  guessMimeType,
  saveAudio,
  sniffMimeType,
} from '../src/media.js';

const bytes = (...xs: number[]) => new Uint8Array(xs);
const ascii = (s: string) => Array.from(s).map((c) => c.charCodeAt(0));
const WAV = new Uint8Array([...ascii('RIFF'), 36, 0, 0, 0, ...ascii('WAVEfmt '), 0, 0, 0, 0]);
const PNG = new Uint8Array([0x89, ...ascii('PNG\r\n'), 0x1a, 0x0a, 0, 0, 0, 0]);
const PDF = new Uint8Array(ascii('%PDF-1.7\n%...'));
const b64 = (u: Uint8Array) => btoa(String.fromCharCode(...u));

describe('mime inference', () => {
  it('from names and urls', () => {
    expect(guessMimeType('A.PNG')).toBe('image/png');
    expect(guessMimeType('photo.jpeg')).toBe('image/jpeg');
    expect(guessMimeType('report.pdf')).toBe('application/pdf');
    expect(guessMimeType('q.wav')).toBe('audio/wav');
    expect(guessMimeType('https://cdn.test/clip.mp4?sig=abc#t=3')).toBe('video/mp4');
    expect(guessMimeType('x.webm')).toBe('video/webm');
    expect(guessMimeType('x.webm', 'audio')).toBe('audio/webm');
    expect(guessMimeType('noextension')).toBeUndefined();
    expect(guessMimeType('https://cdn.test/dir.v2/download')).toBeUndefined();
  });

  it('from magic bytes', () => {
    expect(sniffMimeType(PNG)).toBe('image/png');
    expect(sniffMimeType(bytes(0xff, 0xd8, 0xff, 0xe0, 0, 0, 0, 0))).toBe('image/jpeg');
    expect(sniffMimeType(PDF)).toBe('application/pdf');
    expect(sniffMimeType(WAV)).toBe('audio/wav');
    expect(sniffMimeType(new Uint8Array(ascii('ID3\x04\x00\x00')))).toBe('audio/mpeg');
    expect(sniffMimeType(new Uint8Array(ascii('OggS\0\0\0\0')))).toBe('audio/ogg');
    expect(sniffMimeType(new Uint8Array(ascii('fLaC\0\0\0\0')))).toBe('audio/flac');
    expect(sniffMimeType(new Uint8Array([0, 0, 0, 0x18, ...ascii('ftypM4A '), 0, 0, 0, 0]))).toBe('audio/mp4');
    expect(sniffMimeType(new Uint8Array([0, 0, 0, 0x18, ...ascii('ftypisom'), 0, 0, 0, 0]))).toBe('video/mp4');
    expect(sniffMimeType(new Uint8Array([0, 0, 0, 0x18, ...ascii('ftypisom')]), 'audio')).toBe('audio/mp4');
    expect(sniffMimeType(bytes(0x1a, 0x45, 0xdf, 0xa3, 0))).toBe('video/webm');
    expect(sniffMimeType(new Uint8Array(ascii('just text')))).toBeUndefined();
  });
});

describe('Attachment', () => {
  it('fromBytes infers the type and builds a content part', () => {
    expect(Attachment.fromBytes(PNG, { name: 'cat.png' })).toEqual({
      type: 'image',
      media: { mime_type: 'image/png', data: b64(PNG), name: 'cat.png' },
    });
    expect(Attachment.fromBytes(WAV).type).toBe('audio');
    expect(Attachment.fromBytes(PDF).type).toBe('file');
    expect(Attachment.fromBytes(bytes(1, 2, 3, 4, 5), { mimeType: 'video/mp4' }).type).toBe('video');
    expect(Attachment.fromBytes(PNG.buffer.slice(0) as ArrayBuffer).media.mime_type).toBe('image/png');
    expect(() => Attachment.fromBytes(bytes(1, 2, 3, 4, 5))).toThrow(/pass mimeType/);
  });

  it('drops mime parameters', () => {
    expect(Attachment.fromBytes(bytes(1), { mimeType: 'Audio/WebM;codecs=opus' }).media.mime_type).toBe('audio/webm');
  });

  it('fromUrl keeps the url, no data', () => {
    expect(Attachment.fromUrl('https://cdn.test/photo.jpg', { name: 'photo' })).toEqual({
      type: 'image',
      media: { mime_type: 'image/jpeg', url: 'https://cdn.test/photo.jpg', name: 'photo' },
    });
    expect(() => Attachment.fromUrl('https://cdn.test/download')).toThrow(/pass mimeType/);
    expect(Attachment.fromUrl('https://cdn.test/download', { mimeType: 'application/pdf' }).type).toBe('file');
  });

  it('fromPath reads the file, names it, and falls back to the bytes', async () => {
    const dir = await mkdtemp(join(tmpdir(), 'ao-'));
    await writeFile(join(dir, 'chart.png'), PNG);
    await writeFile(join(dir, 'report.pdf'), PDF);
    await writeFile(join(dir, 'scan'), PNG); // no extension

    expect(await Attachment.fromPath(join(dir, 'chart.png'))).toEqual({
      type: 'image',
      media: { mime_type: 'image/png', data: b64(PNG), name: 'chart.png' },
    });
    expect((await Attachment.fromPath(join(dir, 'report.pdf'))).type).toBe('file');
    expect((await Attachment.fromPath(join(dir, 'scan'))).media.mime_type).toBe('image/png');
    await expect(Attachment.fromPath(join(dir, 'missing.png'))).rejects.toThrow(/cannot read/);
  });
});

describe('audio input and output', () => {
  it('builds AudioInput from bytes, base64 and a path', async () => {
    const want = { data: b64(WAV), mimeType: 'audio/wav' };
    expect(audioFromBytes(WAV)).toEqual(want);
    expect(audioFromBase64(b64(WAV), 'audio/wav')).toEqual(want);
    const dir = await mkdtemp(join(tmpdir(), 'ao-'));
    await writeFile(join(dir, 'q.wav'), WAV);
    expect(await audioFromPath(join(dir, 'q.wav'))).toEqual(want);
    await writeFile(join(dir, 'mic.webm'), bytes(0x1a, 0x45, 0xdf, 0xa3, 0));
    expect((await audioFromPath(join(dir, 'mic.webm'))).mimeType).toBe('audio/webm');
  });

  it('normalises and checks the mime type', () => {
    expect(audioFromBase64('AAAA', 'audio/webm;codecs=opus').mimeType).toBe('audio/webm');
    expect(() => audioFromBase64('AAAA', 'image/png')).toThrow(/audio\//);
    expect(() => audioFromBase64('', 'audio/wav')).toThrow(/empty/);
    expect(() => audioFromBytes(bytes(0, 1, 2))).toThrow(/pass mimeType/);
    expect(audioFromBytes(bytes(0, 1, 2), 'audio/flac').mimeType).toBe('audio/flac');
  });

  it('decodes a reply clip and saves it with the right extension', async () => {
    const mp3 = new Uint8Array([...ascii('ID3'), 4, 0, 1, 2, 3]);
    const clip = audioClipFromWire({ data: b64(mp3), mime_type: 'audio/mpeg' });
    expect(Array.from(clip.data)).toEqual(Array.from(mp3));
    expect(clip.extension).toBe('.mp3');
    expect(audioClipFromWire({ data: 'AA==', mime_type: 'audio/x-unknown' }).extension).toBe('.bin');

    const dir = await mkdtemp(join(tmpdir(), 'ao-'));
    const out = await saveAudio(clip, join(dir, 'answer'));
    expect(out.endsWith('answer.mp3')).toBe(true);
    expect(Array.from(await readFile(out))).toEqual(Array.from(mp3));
    expect((await saveAudio(clip, join(dir, 'keep.bin'))).endsWith('keep.bin')).toBe(true);
  });
});
