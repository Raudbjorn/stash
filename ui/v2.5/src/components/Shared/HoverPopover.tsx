import React, { useState, useCallback, useEffect, useRef } from "react";
import { Overlay, Popover, OverlayProps } from "react-bootstrap";
import { PatchComponent } from "src/patch";
import { Icon } from "./Icon";
import { faExclamationTriangle } from "@fortawesome/free-solid-svg-icons";

interface IHoverPopover {
  enterDelay?: number;
  disabled?: boolean;
  leaveDelay?: number;
  content: JSX.Element[] | JSX.Element | string;
  className?: string;
  placement?: OverlayProps["placement"];
  onOpen?: () => void;
  onClose?: () => void;
  target?: React.RefObject<HTMLElement>;
}

const focusableSelector = [
  "a[href]",
  "button:not([disabled])",
  "input:not([disabled])",
  "select:not([disabled])",
  "textarea:not([disabled])",
  "[tabindex]:not([tabindex='-1'])",
].join(",");

export const HoverPopover: React.FC<IHoverPopover> = PatchComponent(
  "HoverPopover",
  ({
    enterDelay = 200,
    disabled = false,
    leaveDelay = 200,
    content,
    children,
    className,
    placement = "top",
    onOpen,
    onClose,
    target,
  }) => {
    const [show, setShow] = useState(false);
    const [triggerTabIndex, setTriggerTabIndex] = useState<
      number | undefined
    >();
    const triggerRef = useRef<HTMLDivElement | null>(null);
    const popoverRef = useRef<HTMLDivElement | null>(null);
    const enterTimer = useRef<number>();
    const leaveTimer = useRef<number>();
    const focusTimer = useRef<number>();
    const openRef = useRef(false);
    const pointerWithinRef = useRef(false);
    const focusWithinRef = useRef(false);
    const suppressFocusOpenRef = useRef(false);
    const dismissedUntilLeaveRef = useRef(false);

    const clearEnterTimer = useCallback(() => {
      window.clearTimeout(enterTimer.current);
      enterTimer.current = undefined;
    }, []);

    const clearLeaveTimer = useCallback(() => {
      window.clearTimeout(leaveTimer.current);
      leaveTimer.current = undefined;
    }, []);

    const openPopover = useCallback(() => {
      clearEnterTimer();
      clearLeaveTimer();
      if (disabled || openRef.current) return;

      openRef.current = true;
      setShow(true);
      onOpen?.();
    }, [clearEnterTimer, clearLeaveTimer, disabled, onOpen]);

    const closePopover = useCallback(() => {
      clearEnterTimer();
      clearLeaveTimer();
      if (!openRef.current) return;

      openRef.current = false;
      setShow(false);
      onClose?.();
    }, [clearEnterTimer, clearLeaveTimer, onClose]);
    useEffect(() => {
      if (disabled) closePopover();
    }, [closePopover, disabled]);

    const scheduleOpen = useCallback(() => {
      clearLeaveTimer();
      if (
        openRef.current ||
        enterTimer.current !== undefined ||
        dismissedUntilLeaveRef.current
      ) {
        return;
      }

      enterTimer.current = window.setTimeout(() => {
        enterTimer.current = undefined;
        openPopover();
      }, enterDelay);
    }, [clearLeaveTimer, enterDelay, openPopover]);

    const scheduleClose = useCallback(() => {
      clearEnterTimer();
      if (!openRef.current) return;

      clearLeaveTimer();
      leaveTimer.current = window.setTimeout(() => {
        leaveTimer.current = undefined;
        closePopover();
      }, leaveDelay);
    }, [clearEnterTimer, clearLeaveTimer, closePopover, leaveDelay]);

    const isWithinPopover = useCallback((node: EventTarget | null) => {
      return (
        node instanceof Node &&
        (triggerRef.current?.contains(node) ||
          popoverRef.current?.contains(node) ||
          false)
      );
    }, []);

    const handleMouseEnter = useCallback(() => {
      pointerWithinRef.current = true;
      clearLeaveTimer();
      scheduleOpen();
    }, [clearLeaveTimer, scheduleOpen]);

    const handleMouseLeave = useCallback(() => {
      pointerWithinRef.current = false;
      dismissedUntilLeaveRef.current = false;
      clearEnterTimer();
      if (!focusWithinRef.current) scheduleClose();
    }, [clearEnterTimer, scheduleClose]);

    const handleFocus = useCallback(() => {
      focusWithinRef.current = true;
      window.clearTimeout(focusTimer.current);
      if (suppressFocusOpenRef.current) {
        suppressFocusOpenRef.current = false;
        return;
      }

      dismissedUntilLeaveRef.current = false;
      openPopover();
    }, [openPopover]);

    const handleBlur = useCallback(
      (event: React.FocusEvent<HTMLElement> | FocusEvent) => {
        if (isWithinPopover(event.relatedTarget)) return;

        window.clearTimeout(focusTimer.current);
        focusTimer.current = window.setTimeout(() => {
          focusTimer.current = undefined;
          if (isWithinPopover(document.activeElement)) return;

          focusWithinRef.current = false;
          if (!pointerWithinRef.current) scheduleClose();
        });
      },
      [isWithinPopover, scheduleClose]
    );

    const handleKeyDown = useCallback(
      (event: React.KeyboardEvent<HTMLElement> | KeyboardEvent) => {
        if (event.key === "Tab" && event.target instanceof Node) {
          const firstPopoverTarget =
            popoverRef.current?.querySelector<HTMLElement>(focusableSelector);
          if (
            !event.shiftKey &&
            triggerRef.current?.contains(event.target) &&
            firstPopoverTarget
          ) {
            event.preventDefault();
            event.stopPropagation();
            firstPopoverTarget.focus();
            return;
          }

          if (
            event.shiftKey &&
            popoverRef.current?.contains(event.target) &&
            event.target === firstPopoverTarget
          ) {
            event.preventDefault();
            event.stopPropagation();
            const triggerTarget =
              triggerRef.current?.querySelector<HTMLElement>(
                focusableSelector
              ) ?? triggerRef.current;
            triggerTarget?.focus();
            return;
          }
        }

        if (event.key !== "Escape") return;

        event.preventDefault();
        event.stopPropagation();
        dismissedUntilLeaveRef.current = true;
        window.clearTimeout(focusTimer.current);
        closePopover();
      },
      [closePopover]
    );
    const handlePopoverKeyDown = useCallback(
      (event: KeyboardEvent) => {
        const restoreTriggerFocus = event.key === "Escape";
        handleKeyDown(event);
        if (!restoreTriggerFocus) return;

        suppressFocusOpenRef.current = true;
        const focusTarget =
          triggerRef.current?.querySelector<HTMLElement>(focusableSelector) ??
          triggerRef.current;
        focusTimer.current = window.setTimeout(() => {
          focusTimer.current = undefined;
          focusTarget?.focus();
        });
      },
      [handleKeyDown]
    );

    const setTriggerElement = useCallback(
      (element: HTMLDivElement | null) => {
        triggerRef.current?.removeEventListener("focusin", handleFocus);
        triggerRef.current?.removeEventListener("focusout", handleBlur);
        triggerRef.current?.removeEventListener("keydown", handleKeyDown);
        triggerRef.current = element;
        element?.addEventListener("focusin", handleFocus);
        element?.addEventListener("focusout", handleBlur);
        element?.addEventListener("keydown", handleKeyDown);

        if (!element) return;
        const hasFocusableChild =
          element.querySelector(focusableSelector) !== null;
        setTriggerTabIndex(hasFocusableChild ? undefined : 0);
      },
      [handleBlur, handleFocus, handleKeyDown]
    );

    const setPopoverElement = useCallback(
      (element: HTMLDivElement | null) => {
        popoverRef.current?.removeEventListener("focusin", handleFocus);
        popoverRef.current?.removeEventListener("focusout", handleBlur);
        popoverRef.current?.removeEventListener(
          "keydown",
          handlePopoverKeyDown
        );
        popoverRef.current = element;
        element?.addEventListener("focusin", handleFocus);
        element?.addEventListener("focusout", handleBlur);
        element?.addEventListener("keydown", handlePopoverKeyDown);
      },
      [handleBlur, handleFocus, handlePopoverKeyDown]
    );

    useEffect(
      () => () => {
        window.clearTimeout(enterTimer.current);
        window.clearTimeout(leaveTimer.current);
        window.clearTimeout(focusTimer.current);
      },
      []
    );

    return (
      <>
        <div
          className={className}
          onMouseEnter={handleMouseEnter}
          onMouseLeave={handleMouseLeave}
          ref={setTriggerElement}
          tabIndex={triggerTabIndex}
        >
          {children}
        </div>
        {triggerRef.current && (
          <Overlay
            show={show}
            placement={placement}
            target={target?.current ?? triggerRef.current}
          >
            <Popover
              onMouseEnter={handleMouseEnter}
              onMouseLeave={handleMouseLeave}
              id="popover"
              className="hover-popover-content"
            >
              <div ref={setPopoverElement}>{content}</div>
            </Popover>
          </Overlay>
        )}
      </>
    );
  }
);

// convenience component to set the padding on popover content
export const PopoverCard: React.FC<{ className?: string }> = ({
  className,
  children,
}) => {
  return <div className={`popover-card ${className}`}>{children}</div>;
};

export const WarningHoverPopover: React.FC<IHoverPopover> = PatchComponent(
  "WarningHoverPopover",
  ({ children, ...props }) => (
    <HoverPopover {...props} className="warning-hover-popover">
      <Icon icon={faExclamationTriangle} />
    </HoverPopover>
  )
);
