/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 3.2.0
 */

import { useEffect, useState } from 'react';

export type Density = 'compact' | 'regular' | 'comfy';
export type Accent = 'orange' | 'blue' | 'green';

export interface Tweaks {
  density: Density;
  accent: Accent;
  dark: boolean;
}

export const ACCENT_MAP: Record<Accent, Record<string, string>> = {
  orange: {
    '--accent': '#D97757',
    '--accent-hover': '#C4643E',
    '--accent-bg': '#FDF3EE',
    '--accent-100': '#F9DFD1',
    '--accent-200': '#F0C5A8',
    '--accent-300': '#E5A57E',
  },
  blue: {
    '--accent': '#3B6FD4',
    '--accent-hover': '#2A56AB',
    '--accent-bg': '#EEF4FF',
    '--accent-100': '#D5E2FA',
    '--accent-200': '#A8C0EE',
    '--accent-300': '#7396DD',
  },
  green: {
    '--accent': '#2D7D46',
    '--accent-hover': '#1E5730',
    '--accent-bg': '#EDFBF2',
    '--accent-100': '#C8EBD3',
    '--accent-200': '#9CD4AF',
    '--accent-300': '#6BB785',
  },
};

const STORAGE_KEY = 'ccm.tweaks';

const DEFAULTS: Tweaks = { density: 'regular', accent: 'orange', dark: false };

export function loadTweaks(): Tweaks {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (!raw) return DEFAULTS;
    const parsed = JSON.parse(raw) as Partial<Tweaks>;
    return { ...DEFAULTS, ...parsed };
  } catch {
    return DEFAULTS;
  }
}

export function applyTweaks(t: Tweaks): void {
  document.body.dataset.density = t.density;
  document.body.dataset.theme = t.dark ? 'dark' : 'light';
  const accent = ACCENT_MAP[t.accent] || ACCENT_MAP.orange;
  for (const [k, v] of Object.entries(accent)) {
    document.body.style.setProperty(k, v);
  }
}

export function useTweaks(): [Tweaks, <K extends keyof Tweaks>(key: K, value: Tweaks[K]) => void] {
  const [t, setT] = useState<Tweaks>(loadTweaks);

  useEffect(() => {
    applyTweaks(t);
    try {
      localStorage.setItem(STORAGE_KEY, JSON.stringify(t));
    } catch {
      // ignore quota errors
    }
  }, [t]);

  function setTweak<K extends keyof Tweaks>(key: K, value: Tweaks[K]) {
    setT(prev => ({ ...prev, [key]: value }));
  }

  return [t, setTweak];
}
