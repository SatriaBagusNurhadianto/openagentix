import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it } from 'vitest';
import SignIn from './SignIn.svelte';

describe('sign-in token field', () => {
    it('is a password field that browsers are told not to remember', () => {
        render(SignIn);
        const input = screen.getByLabelText('Console token');
        expect(input).toHaveAttribute('type', 'password');
        expect(input).toHaveAttribute('autocomplete', 'off');
    });

    it('has a show and hide control that says what it does', async () => {
        render(SignIn);
        const input = screen.getByLabelText('Console token');
        const toggle = screen.getByRole('button', { name: 'Show token' });
        expect(toggle).toHaveAttribute('aria-pressed', 'false');
        await userEvent.click(toggle);
        expect(input).toHaveAttribute('type', 'text');
        const hide = screen.getByRole('button', { name: 'Hide token' });
        expect(hide).toHaveAttribute('aria-pressed', 'true');
        await userEvent.click(hide);
        expect(input).toHaveAttribute('type', 'password');
    });

    it('does not submit the form when the toggle is pressed', async () => {
        render(SignIn);
        await userEvent.click(screen.getByRole('button', { name: 'Show token' }));
        expect(screen.queryByText('Enter the console token.')).not.toBeInTheDocument();
    });
});
