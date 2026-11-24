import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { Markdown } from './Markdown';

describe('Markdown', () => {
  it('formats headings, lists, code and emphasis, and keeps HTML inert', () => {
    render(<Markdown label="reply" text={'## Title\n\n- one **bold**\n- two `x`\n\n```\nterraform plan\n```\n<img src=x onerror=alert(1)> [a](http://e.x)'} />);
    const box = screen.getByLabelText('reply');
    expect(box.querySelector('h4')?.textContent).toBe('Title');
    expect(box.querySelectorAll('li')).toHaveLength(2);
    expect(box.querySelector('strong')?.textContent).toBe('bold');
    expect(box.querySelector('pre code')?.textContent).toBe('terraform plan');
    expect(box.querySelector('img, a')).toBeNull();
    expect(box.textContent).toContain('<img src=x onerror=alert(1)>');
  });
});
