import { useState } from 'react';
import { Icon } from './Icon';
import { ACCENT_MAP } from './tweaks';
import type { Tweaks } from './tweaks';

interface PanelProps {
  tweaks: Tweaks;
  setTweak: <K extends keyof Tweaks>(key: K, value: Tweaks[K]) => void;
}

export function TweaksPanel({ tweaks, setTweak }: PanelProps) {
  const [open, setOpen] = useState(false);

  if (!open) {
    return (
      <button
        className="twk-fab"
        aria-label="打开 tweaks 面板"
        title="Tweaks"
        onClick={() => setOpen(true)}
      >
        <Icon name="settings" size={18} />
      </button>
    );
  }

  return (
    <div className="twk-panel" role="dialog" aria-label="Tweaks">
      <div className="twk-hd">
        <b>Tweaks</b>
        <button
          className="twk-x"
          aria-label="关闭"
          onClick={() => setOpen(false)}
        >
          ✕
        </button>
      </div>
      <div className="twk-body">
        <div className="twk-sect">布局</div>
        <div className="twk-row">
          <span className="twk-lbl">卡片密度</span>
          <div className="twk-seg" role="radiogroup" aria-label="卡片密度">
            {(
              [
                ['compact', '紧凑'],
                ['regular', '常规'],
                ['comfy', '宽松'],
              ] as const
            ).map(([v, label]) => (
              <button
                key={v}
                type="button"
                role="radio"
                aria-checked={tweaks.density === v}
                onClick={() => setTweak('density', v)}
              >
                {label}
              </button>
            ))}
          </div>
        </div>

        <div className="twk-sect">主题</div>
        <div className="twk-row">
          <span className="twk-lbl">强调色</span>
          <div className="twk-chips" role="radiogroup" aria-label="强调色">
            {(['orange', 'blue', 'green'] as const).map(a => (
              <button
                key={a}
                type="button"
                role="radio"
                aria-checked={tweaks.accent === a}
                data-on={tweaks.accent === a ? '1' : '0'}
                aria-label={a}
                title={a}
                style={{ background: ACCENT_MAP[a]['--accent'] }}
                onClick={() => setTweak('accent', a)}
              />
            ))}
          </div>
        </div>

        <div className="twk-row twk-row-h">
          <span className="twk-lbl">深色模式</span>
          <button
            type="button"
            className="twk-toggle"
            role="switch"
            aria-checked={tweaks.dark}
            data-on={tweaks.dark ? '1' : '0'}
            onClick={() => setTweak('dark', !tweaks.dark)}
          >
            <i />
          </button>
        </div>
      </div>
    </div>
  );
}
