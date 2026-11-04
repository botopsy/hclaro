// shadcn/ui Button pattern (Radix Slot + class-variance-authority), styled for Hclaro.
import * as React from 'react';
import { Slot } from '@radix-ui/react-slot';
import { cva, type VariantProps } from 'class-variance-authority';
import { clsx, type ClassValue } from 'clsx';
import { twMerge } from 'tailwind-merge';
function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}
const buttonVariants = cva('button', {
  variants: {
    variant: {
      default: 'button-default',
      ghost: 'button-ghost',
      outline: 'button-outline'
    },
    size: {
      default: '',
      icon: 'button-icon',
      small: 'button-small'
    }
  },
  defaultVariants: {
    variant: 'default',
    size: 'default'
  }
});
type Props = React.ButtonHTMLAttributes<HTMLButtonElement> & VariantProps<typeof buttonVariants> & {
  asChild?: boolean;
};
export function Button({
  className,
  variant,
  size,
  asChild = false,
  ...props
}: Props) {
  const Comp = asChild ? Slot : 'button';
  return <Comp className={cn(buttonVariants({
    variant,
    size,
    className
  }))} {...props} />;
}
