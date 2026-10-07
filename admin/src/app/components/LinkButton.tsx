"use client";

import Link from "next/link";
import type { ComponentType } from "react";
import { Button, type ButtonProps } from "react-bootstrap";

type Props = Omit<ButtonProps, "as"> & { href: string };

// Bootstrap 2's ButtonProps narrows `as` to HTML tags, although the component
// supports React components. Keep that type correction inside this adapter.
const BootstrapLinkButton = Button as ComponentType<
  Props & { as: typeof Link }
>;

export default function LinkButton(props: Props) {
  return <BootstrapLinkButton as={Link} {...props} />;
}
