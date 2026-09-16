<?php

declare(strict_types=1);

namespace Demo;

/**
 * @method static self zero()
 */
final class Money
{
    public function __construct(public readonly int $cents)
    {
    }
}
